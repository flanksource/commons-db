package connections

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/flanksource/commons-db/connection/crd"
	"github.com/flanksource/commons-db/connection/store"
	"github.com/flanksource/commons-db/models"
	"github.com/google/uuid"
)

func (p *StoreProvider) DefaultLayer() string { return p.layer.Name }

func (p *StoreProvider) StorageLayers() []StorageLayer {
	result := []StorageLayer{}
	for _, layer := range p.store.Layers() {
		result = append(result, StorageLayer{Name: layer.Name, Label: map[string]string{"global": "Global files", "app": "Application files", "cwd": "Project files"}[layer.Name], Path: layer.Dir, Writable: (layer.Name == p.layer.Name || slices.Contains(p.writableLayers, layer.Name)) && dirWritable(layer.Dir)})
	}
	return result
}

func (p *StoreProvider) saveToLayer(ctx context.Context, c *models.Connection) (*models.Connection, error) {
	layer := c.Layer
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(p.StorageLayers(), func(l StorageLayer) bool { return l.Name == layer && l.Writable }) {
		return nil, fmt.Errorf("connection layer %q is unavailable or read-only", layer)
	}
	if err := validateConnectionName(c.Name); err != nil {
		return nil, err
	}
	p.writes.Lock()
	defer p.writes.Unlock()
	var previous *store.Entry
	var err error
	if c.ID != uuid.Nil {
		previous, err = p.store.Find(c.ID.String())
		if err != nil {
			return nil, err
		}
	}
	if previous != nil && previous.Layer.Name == layer {
		return nil, fmt.Errorf("connection is already stored in layer %q", layer)
	}
	if previous != nil {
		if previous.Connection.ReadOnly || !slices.ContainsFunc(p.StorageLayers(), func(l StorageLayer) bool { return l.Name == previous.Layer.Name && l.Writable }) {
			return nil, readOnlyLayer(*previous)
		}
		if p.validateDelete != nil {
			if err := p.validateDelete(ctx, previous.Connection); err != nil {
				return nil, err
			}
		}
	}
	targetLayers := slices.DeleteFunc(p.store.Layers(), func(l store.Layer) bool { return l.Name != layer })
	target, err := store.New(targetLayers...)
	if err != nil {
		return nil, err
	}
	identity := "connection://" + c.Namespace + "/" + c.Name
	existing, err := target.Find(identity)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, fmt.Errorf("connection %q already exists in layer %q", c.Name, layer)
	}
	if c.ID != uuid.Nil {
		byID, err := target.Find(c.ID.String())
		if err != nil {
			return nil, err
		}
		if byID != nil {
			return nil, fmt.Errorf("connection ID already exists in layer %q", layer)
		}
	}
	resource, err := crd.FromModel(*c)
	if err != nil {
		return nil, err
	}
	if err := target.Save(layer, resource); err != nil {
		return nil, err
	}
	if previous != nil {
		if err := p.store.Delete(previous.Layer.Name, previous.Connection.ID.String()); err != nil {
			return nil, errors.Join(err, target.Delete(layer, identity))
		}
	}
	saved, err := target.Find(identity)
	if err != nil {
		return nil, err
	}
	if saved == nil {
		return nil, fmt.Errorf("saved connection %q cannot be resolved", c.Name)
	}
	return p.project(*saved, true), nil
}
