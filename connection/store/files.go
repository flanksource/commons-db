package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/uuid"
	"sigs.k8s.io/yaml"

	"github.com/flanksource/commons-db/connection/crd"
	"github.com/flanksource/commons-db/models"
)

// extensions are the files a layer's connections are read from.
var extensions = []string{".yaml", ".yml", ".json"}

// entry is one connection a layer's file defines.
type entry struct {
	layer      Layer
	path       string
	resource   *crd.Connection
	connection models.Connection
}

func (e entry) key() string { return e.connection.Namespace + "/" + e.connection.Name }

// readLayer reads every connection file in layer; a layer whose dir does not
// exist has none.
func readLayer(layer Layer) ([]entry, error) {
	files, err := os.ReadDir(layer.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s connections: %w", layer.Name, err)
	}
	var entries []entry
	byKey := map[string]string{}
	for _, file := range files {
		if file.IsDir() || strings.HasPrefix(file.Name(), ".") || !slices.Contains(extensions, filepath.Ext(file.Name())) {
			continue
		}
		path := filepath.Join(layer.Dir, file.Name())
		read, err := readFile(layer, path)
		if err != nil {
			return nil, err
		}
		if previous, ok := byKey[read.key()]; ok {
			return nil, fmt.Errorf("%s and %s both define connection %s", previous, path, read.key())
		}
		byKey[read.key()] = path
		entries = append(entries, read)
	}
	return entries, nil
}

func readFile(layer Layer, path string) (entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return entry{}, fmt.Errorf("read connection %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return entry{}, fmt.Errorf("read connection %s: %w", path, err)
	}
	var resource crd.Connection
	if err := yaml.UnmarshalStrict(data, &resource); err != nil {
		return entry{}, fmt.Errorf("decode connection %s: %w", path, err)
	}
	if resource.Name == "" {
		resource.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	connection, err := crd.ToModel(&resource)
	if err != nil {
		return entry{}, fmt.Errorf("connection %s: %w", path, err)
	}
	connection.ID = connectionID(layer, connection)
	connection.UpdatedAt = info.ModTime()
	connection.CreatedAt = info.ModTime()
	connection.Source = "file://" + path
	connection.Virtual = true
	return entry{layer: layer, path: path, resource: &resource, connection: connection}, nil
}

// connectionID is stable for a connection of a layer however often, or after
// whichever edit, its file is read.
func connectionID(layer Layer, connection models.Connection) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("connection://"+layer.Name+"/"+connection.Namespace+"/"+connection.Name))
}

// writeFile writes resource to path, readable by its owner alone: a
// connection's file can hold credentials.
func writeFile(path string, resource *crd.Connection) error {
	data, err := yaml.Marshal(resource)
	if err != nil {
		return fmt.Errorf("encode connection %s: %w", resource.Name, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("save connection %s: %w", resource.Name, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("save connection %s: %w", resource.Name, err)
	}
	// WriteFile keeps the mode of a file it overwrites.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("save connection %s: %w", resource.Name, err)
	}
	return nil
}
