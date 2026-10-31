package store

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/flanksource/commons-db/connection/crd"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
)

// Store is the connections its layers' files define, a later layer's
// connection replacing an earlier one's of the same namespace and name. It
// reads the files on every call, so an edit applies at once.
type Store struct {
	layers []Layer
}

// New is the store over layers, in override order.
func New(layers ...Layer) (*Store, error) {
	if len(layers) == 0 {
		return nil, fmt.Errorf("connection store: at least one layer is required")
	}
	resolved := make([]Layer, 0, len(layers))
	for _, layer := range layers {
		if layer.Name == "" || layer.Dir == "" {
			return nil, fmt.Errorf("connection store: layer %+v needs a name and a dir", layer)
		}
		if slices.ContainsFunc(resolved, func(other Layer) bool { return other.Name == layer.Name }) {
			return nil, fmt.Errorf("connection store: layer %q is given twice", layer.Name)
		}
		dir, err := filepath.Abs(layer.Dir)
		if err != nil {
			return nil, fmt.Errorf("connection store: layer %s: %w", layer.Name, err)
		}
		resolved = append(resolved, Layer{Name: layer.Name, Dir: dir})
	}
	return &Store{layers: resolved}, nil
}

// Open is the store over DefaultLayers(app).
func Open(app string) (*Store, error) {
	layers, err := DefaultLayers(app)
	if err != nil {
		return nil, err
	}
	return New(layers...)
}

// Layers are the store's layers, in override order.
func (s *Store) Layers() []Layer { return slices.Clone(s.layers) }

// entries are the connections in effect, each its last layer's.
func (s *Store) entries() ([]Entry, error) {
	effective := map[string]Entry{}
	for _, layer := range s.layers {
		entries, err := readLayer(layer)
		if err != nil {
			return nil, err
		}
		for _, read := range entries {
			effective[read.key()] = read
		}
	}
	merged := make([]Entry, 0, len(effective))
	for _, read := range effective {
		merged = append(merged, read)
	}
	slices.SortFunc(merged, func(a, b Entry) int { return strings.Compare(a.key(), b.key()) })
	return merged, nil
}

// Entries are the entries in effect, by namespace and name, each with the
// layer and file that define it.
func (s *Store) Entries() ([]Entry, error) { return s.entries() }

// ListConnections are the connections in effect, by namespace and name.
func (s *Store) ListConnections() ([]*models.Connection, error) {
	entries, err := s.entries()
	if err != nil {
		return nil, err
	}
	connections := make([]*models.Connection, 0, len(entries))
	for _, read := range entries {
		connections = append(connections, &read.Connection)
	}
	return connections, nil
}

// ResolveConnection is the connection reference names: its id,
// connection://name, connection://namespace/name, the deprecated
// connection://type/namespace/name, or a bare name. A connection with no
// namespace answers any namespace a reference names that defines no connection
// of that name. It is (nil, nil) for a reference the store
// does not define, and an error for a name more than one namespace defines.
// A file that defines no usable connection resolves to a connection carrying
// the problem in Warnings, which dbcontext.HydrateConnection refuses to use.
// It is a dbcontext.ConnectionResolver.
func (s *Store) ResolveConnection(_ dbcontext.Context, reference string) (*models.Connection, error) {
	read, err := s.Find(reference)
	if err != nil || read == nil {
		return nil, err
	}
	return &read.Connection, nil
}

// Find is the entry in effect that reference names, with the layer and file
// that define it, on the terms of ResolveConnection: (nil, nil) for a
// reference the store does not define.
func (s *Store) Find(reference string) (*Entry, error) {
	return s.find(reference, s.entries)
}

func (s *Store) find(reference string, entries func() ([]Entry, error)) (*Entry, error) {
	all, err := entries()
	if err != nil {
		return nil, err
	}
	id, idErr := uuid.Parse(reference)
	namespace, name, named := parseReference(reference)
	var matches []Entry
	for _, read := range all {
		switch {
		case idErr == nil && read.Connection.ID == id,
			named && read.Connection.Name == name && (namespace == "" || read.Connection.Namespace == "" || read.Connection.Namespace == namespace):
			matches = append(matches, read)
		}
	}
	if namespace != "" {
		exact := slices.DeleteFunc(slices.Clone(matches), func(match Entry) bool { return match.Connection.Namespace != namespace })
		if len(exact) > 0 {
			matches = exact
		}
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return &matches[0], nil
	}
	sources := make([]string, 0, len(matches))
	for _, match := range matches {
		sources = append(sources, match.Path)
	}
	return nil, fmt.Errorf("connection %q is ambiguous: %s each define one; name its namespace", reference, strings.Join(sources, ", "))
}

// parseReference reads connection://[type/][namespace/]name or a bare name;
// a URL of any other scheme names no stored connection.
func parseReference(reference string) (namespace, name string, ok bool) {
	rest, prefixed := strings.CutPrefix(reference, "connection://")
	if !prefixed && strings.Contains(reference, "://") {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	switch len(parts) {
	case 1:
		name = parts[0]
	case 2:
		namespace, name = parts[0], parts[1]
	case 3:
		namespace, name = parts[1], parts[2]
	default:
		return "", "", false
	}
	return strings.TrimSpace(namespace), strings.TrimSpace(name), strings.TrimSpace(name) != ""
}

// Attach is ctx answering connection references from the store first, then
// from the resolver ctx already had, then from the connections table.
func (s *Store) Attach(ctx dbcontext.Context) dbcontext.Context {
	next := ctx.ConnectionResolver()
	return ctx.WithConnectionResolver(func(ctx dbcontext.Context, reference string) (*models.Connection, error) {
		connection, err := s.ResolveConnection(ctx, reference)
		if err != nil || connection != nil || next == nil {
			return connection, err
		}
		return next(ctx, reference)
	})
}

// Save writes resource into layer, over the file that already defines its
// namespace and name there, else as <name>.yaml.
func (s *Store) Save(layer string, resource *crd.Connection) error {
	target, err := s.layer(layer)
	if err != nil {
		return err
	}
	if resource.Name == "" {
		return fmt.Errorf("save connection: metadata.name is required")
	}
	if resource.Name != filepath.Base(resource.Name) || strings.HasPrefix(resource.Name, ".") {
		return fmt.Errorf("save connection: metadata.name %q must be a plain file name that does not start with a dot", resource.Name)
	}
	resource.APIVersion, resource.Kind = crd.APIVersion, crd.Kind
	if _, err := crd.ToModel(resource); err != nil {
		return fmt.Errorf("save connection %s: %w", resource.Name, err)
	}
	path := filepath.Join(target.Dir, resource.Name+".yaml")
	existing, err := s.find("connection://"+resource.Namespace+"/"+resource.Name, func() ([]Entry, error) { return readLayer(target) })
	if err != nil {
		return err
	}
	if existing != nil && existing.Connection.Namespace == resource.Namespace {
		path = existing.Path
	} else if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("save connection %s: %s already defines another connection", resource.Name, path)
	}
	return writeFile(path, resource)
}

// Delete removes the file in layer that defines the connection reference
// names; one the layer does not define is os.ErrNotExist.
func (s *Store) Delete(layer, reference string) error {
	target, err := s.layer(layer)
	if err != nil {
		return err
	}
	existing, err := s.find(reference, func() ([]Entry, error) { return readLayer(target) })
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("delete connection %s from %s: %w", reference, layer, os.ErrNotExist)
	}
	if err := os.Remove(existing.Path); err != nil {
		return fmt.Errorf("delete connection %s: %w", reference, err)
	}
	return nil
}

func (s *Store) layer(name string) (Layer, error) {
	for _, layer := range s.layers {
		if layer.Name == name {
			return layer, nil
		}
	}
	return Layer{}, fmt.Errorf("connection store has no layer %q", name)
}
