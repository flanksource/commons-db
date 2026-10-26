package connections

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/commons-db/connection/crd"
	"github.com/flanksource/commons-db/connection/store"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/google/uuid"
)

// StoreProviderOptions configure a StoreProvider.
type StoreProviderOptions struct {
	// Store holds the connection files, in layers.
	Store *store.Store
	// WriteLayer names the store layer connections are created and edited in;
	// every other layer's connections are read-only.
	WriteLayer string
	// ValidateDelete, when set, may veto deleting a connection, such as one
	// the host's own configuration still names.
	ValidateDelete func(context.Context, models.Connection) error
}

// StoreProvider exposes a layered connection store (connection/store) to the
// connection service: it lists and resolves every layer's connections and
// writes those of its WriteLayer as typed Connection files.
type StoreProvider struct {
	store          *store.Store
	layer          store.Layer
	validateDelete func(context.Context, models.Connection) error
	writes         sync.Mutex
}

// NewStoreProvider is the provider over options.Store, writing to its
// options.WriteLayer layer.
func NewStoreProvider(options StoreProviderOptions) (*StoreProvider, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("store provider: a connection store is required")
	}
	index := slices.IndexFunc(options.Store.Layers(), func(layer store.Layer) bool { return layer.Name == options.WriteLayer })
	if index < 0 {
		return nil, fmt.Errorf("store provider: the connection store has no layer %q to write to", options.WriteLayer)
	}
	return &StoreProvider{store: options.Store, layer: options.Store.Layers()[index], validateDelete: options.ValidateDelete}, nil
}

// Writable reports whether the write layer's directory accepts files.
func (p *StoreProvider) Writable() bool { return dirWritable(p.layer.Dir) }

func (p *StoreProvider) ListConnections() ([]*models.Connection, error) {
	entries, err := p.store.Entries()
	if err != nil {
		return nil, err
	}
	writable := p.Writable()
	listed := make([]*models.Connection, 0, len(entries))
	for _, entry := range entries {
		listed = append(listed, p.project(entry, writable))
	}
	return listed, nil
}

func (p *StoreProvider) ResolveConnection(_ dbcontext.Context, reference string) (*models.Connection, error) {
	entry, err := p.store.Find(reference)
	if err != nil || entry == nil {
		return nil, err
	}
	return p.project(*entry, p.Writable()), nil
}

// project is entry's connection as the service sees it: a write-layer
// connection is editable while the layer is writable; another layer's stays
// virtual and read-only, so the service refuses to edit it.
func (p *StoreProvider) project(entry store.Entry, writable bool) *models.Connection {
	connection := entry.Connection
	connection.Properties = maps.Clone(connection.Properties)
	if entry.Layer.Name == p.layer.Name {
		connection.Virtual, connection.ReadOnly = false, !writable
	} else {
		connection.Virtual, connection.ReadOnly = true, true
	}
	return &connection
}

// SaveConnection creates connection (a zero ID) or updates the connection its
// ID names, renaming its file when its name changes.
func (p *StoreProvider) SaveConnection(ctx context.Context, connection *models.Connection) (*models.Connection, error) {
	if err := validateConnectionName(connection.Name); err != nil {
		return nil, err
	}
	p.writes.Lock()
	defer p.writes.Unlock()
	if err := p.writableNow(ctx); err != nil {
		return nil, err
	}
	previous, err := p.previous(connection.ID)
	if err != nil {
		return nil, err
	}
	if err := p.refuseTakenName(connection, previous); err != nil {
		return nil, err
	}
	candidate := *connection
	candidate.ID = uuid.Nil
	resource, err := crd.FromModel(candidate)
	if err != nil {
		return nil, entity.NewStatusErrorf(http.StatusBadRequest, "invalid_connection", "%v", err)
	}
	if err := p.write(previous, resource); err != nil {
		return nil, err
	}
	saved, err := p.store.Find("connection://" + resource.Namespace + "/" + resource.Name)
	if err != nil {
		return nil, err
	}
	if saved == nil {
		return nil, fmt.Errorf("connection %q was saved but the store does not resolve it", resource.Name)
	}
	return p.project(*saved, true), nil
}

// DeleteConnection removes the write layer's file for the connection id names.
func (p *StoreProvider) DeleteConnection(ctx context.Context, id string) error {
	p.writes.Lock()
	defer p.writes.Unlock()
	if err := p.writableNow(ctx); err != nil {
		return err
	}
	entry, err := p.store.Find(id)
	if err != nil {
		return err
	}
	if entry == nil {
		return entity.NewStatusErrorf(http.StatusNotFound, "not_found", "connection %q not found", id)
	}
	if entry.Layer.Name != p.layer.Name {
		return readOnlyLayer(*entry)
	}
	if p.validateDelete != nil {
		if err := p.validateDelete(ctx, entry.Connection); err != nil {
			return err
		}
	}
	return p.store.Delete(p.layer.Name, entry.Connection.ID.String())
}

func (p *StoreProvider) writableNow(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !p.Writable() {
		return entity.NewStatusErrorf(http.StatusMethodNotAllowed, "read_only", "connections in %s are read-only", p.layer.Dir)
	}
	return nil
}

// previous is the write-layer entry id names, nil for a new connection.
func (p *StoreProvider) previous(id uuid.UUID) (*store.Entry, error) {
	if id == uuid.Nil {
		return nil, nil
	}
	entry, err := p.store.Find(id.String())
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, entity.NewStatusErrorf(http.StatusNotFound, "not_found", "connection %q not found", id)
	}
	if entry.Layer.Name != p.layer.Name {
		return nil, readOnlyLayer(*entry)
	}
	return entry, nil
}

// refuseTakenName refuses a name that a connection other than previous, in
// any layer, already answers to.
func (p *StoreProvider) refuseTakenName(connection *models.Connection, previous *store.Entry) error {
	existing, err := p.store.Find("connection://" + connection.Namespace + "/" + connection.Name)
	if err != nil {
		return err
	}
	if existing != nil && (previous == nil || existing.Path != previous.Path) {
		return entity.NewStatusErrorf(http.StatusConflict, "already_exists", "connection %q already exists in %s", connection.Name, existing.Path)
	}
	return nil
}

// write saves resource into the write layer. An edit that keeps previous's
// namespace and name overwrites its file; one that moves it to another file
// (a rename) writes that file and then removes previous's; one that keeps the
// file but changes the namespace, which the store refuses to overwrite,
// replaces it.
func (p *StoreProvider) write(previous *store.Entry, resource *crd.Connection) error {
	switch {
	case previous == nil,
		previous.Connection.Namespace == resource.Namespace && previous.Connection.Name == resource.Name:
		return p.store.Save(p.layer.Name, resource)
	case previous.Path == filepath.Join(p.layer.Dir, resource.Name+".yaml"):
		return p.replace(*previous, resource)
	}
	if err := p.store.Save(p.layer.Name, resource); err != nil {
		return err
	}
	return p.store.Delete(p.layer.Name, previous.Connection.ID.String())
}

// replace swaps previous's file for resource, restoring the file when the
// save fails.
func (p *StoreProvider) replace(previous store.Entry, resource *crd.Connection) error {
	backup, err := os.ReadFile(previous.Path)
	if err != nil {
		return fmt.Errorf("replace connection %s: %w", previous.Path, err)
	}
	if err := p.store.Delete(p.layer.Name, previous.Connection.ID.String()); err != nil {
		return err
	}
	if err := p.store.Save(p.layer.Name, resource); err != nil {
		return errors.Join(err, os.WriteFile(previous.Path, backup, 0o600))
	}
	return nil
}

func readOnlyLayer(entry store.Entry) error {
	return entity.NewStatusErrorf(http.StatusMethodNotAllowed, "read_only",
		"connection %q is defined in the %s layer (%s), which is read-only here", entry.Connection.Name, entry.Layer.Name, entry.Path)
}

func validateConnectionName(name string) error {
	if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, `/\#`) || name == "." || name == ".." {
		return entity.NewStatusErrorf(http.StatusBadRequest, "invalid_name",
			"connection name %q must be non-empty and must not contain '/', '\\', '#' or surrounding whitespace", name)
	}
	return nil
}

// dirWritable reports whether files can be created in dir, or in its nearest
// existing ancestor when dir does not exist yet.
func dirWritable(dir string) bool {
	for {
		info, err := os.Stat(dir)
		if err == nil {
			if !info.IsDir() {
				return false
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(dir) == dir {
			return false
		}
		dir = filepath.Dir(dir)
	}
	probe, err := os.CreateTemp(dir, ".writable-probe-*")
	if err != nil {
		return false
	}
	return probe.Close() == nil && os.Remove(probe.Name()) == nil
}
