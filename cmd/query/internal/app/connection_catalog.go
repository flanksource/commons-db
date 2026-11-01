package app

import (
	"path/filepath"

	"github.com/flanksource/commons-db/cmd/query/connections"
	"github.com/flanksource/commons-db/connection/store"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
)

type connectionCatalog struct {
	files     *store.Store
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
	return &connectionCatalog{files: files, snapshots: snapshots}, nil
}

func (c *connectionCatalog) ListConnections() ([]*models.Connection, error) {
	files, err := c.files.ListConnections()
	if err != nil {
		return nil, err
	}
	snapshots, err := c.snapshots.ListConnections()
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		file.ReadOnly = true
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
	file, err := c.files.ResolveConnection(ctx, reference)
	if file != nil {
		file.ReadOnly = true
	}
	if file != nil || err != nil {
		return file, err
	}
	return c.snapshots.ResolveConnection(ctx, reference)
}
