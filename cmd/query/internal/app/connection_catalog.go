package app

import (
	"context"
	"path/filepath"

	"github.com/flanksource/commons-db/cmd/query/connections"
	"github.com/flanksource/commons-db/connection/store"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
)

type connectionCatalog struct {
	files     *store.Store
	writer    *connections.StoreProvider
	snapshots connections.VirtualConnectionProvider
}

func newConnectionCatalog(configDir string, snapshots connections.VirtualConnectionProvider) (*connectionCatalog, error) {
	layers, err := store.DefaultLayers("query")
	if err != nil {
		return nil, err
	}
	layers[1].Dir = filepath.Join(configDir, "connections")
	files, err := store.New(layers...)
	if err != nil {
		return nil, err
	}
	writer, err := connections.NewStoreProvider(connections.StoreProviderOptions{Store: files, WriteLayer: store.LayerApp, WritableLayers: []string{store.LayerGlobal, store.LayerApp, store.LayerCWD}})
	if err != nil {
		return nil, err
	}
	return &connectionCatalog{files: files, writer: writer, snapshots: snapshots}, nil
}

func (c *connectionCatalog) ListConnections() ([]*models.Connection, error) {
	files, err := c.writer.ListConnections()
	if err != nil {
		return nil, err
	}
	snapshots, err := c.snapshots.ListConnections()
	if err != nil {
		return nil, err
	}
	for _, snapshot := range snapshots {
		found := false
		for _, file := range files {
			if file.Name == snapshot.Name && file.Namespace == snapshot.Namespace {
				found = true
				break
			}
		}
		if !found {
			files = append(files, snapshot)
		}
	}
	return files, nil
}

func (c *connectionCatalog) ResolveConnection(ctx dbcontext.Context, reference string) (*models.Connection, error) {
	file, err := c.writer.ResolveConnection(ctx, reference)
	if file != nil || err != nil {
		return file, err
	}
	return c.snapshots.ResolveConnection(ctx, reference)
}

func (c *connectionCatalog) Writable() bool       { return c.writer.Writable() }
func (c *connectionCatalog) DefaultLayer() string { return c.writer.DefaultLayer() }
func (c *connectionCatalog) StorageLayers() []connections.StorageLayer {
	return c.writer.StorageLayers()
}
func (c *connectionCatalog) SaveConnection(ctx context.Context, value *models.Connection) (*models.Connection, error) {
	return c.writer.SaveConnection(ctx, value)
}
func (c *connectionCatalog) DeleteConnection(ctx context.Context, id string) error {
	return c.writer.DeleteConnection(ctx, id)
}
