package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/flanksource/commons-db/connection/crd"
	"github.com/flanksource/commons-db/models"
)

// extensions are the files a layer's connections are read from.
var extensions = []string{".yaml", ".yml", ".json"}

// Entry is one connection a layer's file defines.
type Entry struct {
	Layer      Layer
	Path       string
	Connection models.Connection
	// Problem is why the file does not define a usable connection; the
	// connection then carries it in Warnings, and hydrating it fails.
	Problem error
}

func (e Entry) key() string { return e.Connection.Namespace + "/" + e.Connection.Name }

// readLayer reads every connection file in layer; a layer whose dir does not
// exist has none. A file that defines no usable connection is still an entry,
// carrying its problem, so it is reported on its own connection without hiding
// the others.
func readLayer(layer Layer) ([]Entry, error) {
	files, err := os.ReadDir(layer.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s connections: %w", layer.Name, err)
	}
	var entries []Entry
	byKey := map[string]int{}
	for _, file := range files {
		if file.IsDir() || strings.HasPrefix(file.Name(), ".") || !slices.Contains(extensions, filepath.Ext(file.Name())) {
			continue
		}
		path := filepath.Join(layer.Dir, file.Name())
		read, err := readFile(layer, path)
		if err != nil {
			read = brokenEntry(layer, path, err)
		}
		if previous, ok := byKey[read.key()]; ok {
			first := &entries[previous]
			first.Connection.Warnings = append(first.Connection.Warnings, fmt.Sprintf("%s also defines connection %s and is ignored", path, read.key()))
			continue
		}
		byKey[read.key()] = len(entries)
		entries = append(entries, read)
	}
	return entries, nil
}

// brokenEntry is the connection a file names (metadata, else the file stem)
// when the file does not define a usable one.
func brokenEntry(layer Layer, path string, problem error) Entry {
	var resource struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = yaml.Unmarshal(data, &resource)
	}
	connection := models.Connection{
		Name:      resource.Metadata.Name,
		Namespace: resource.Metadata.Namespace,
		Source:    "file://" + path,
		Virtual:   true,
		Warnings:  []string{problem.Error()},
	}
	if connection.Name == "" {
		connection.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	connection.ID = connectionID(layer, connection)
	if info, err := os.Stat(path); err == nil {
		connection.UpdatedAt, connection.CreatedAt = info.ModTime(), info.ModTime()
	}
	return Entry{Layer: layer, Path: path, Connection: connection, Problem: problem}
}

func readFile(layer Layer, path string) (Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, fmt.Errorf("read connection %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return Entry{}, fmt.Errorf("read connection %s: %w", path, err)
	}
	var header metav1.TypeMeta
	if err := yaml.Unmarshal(data, &header); err != nil {
		return Entry{}, fmt.Errorf("decode connection %s: %w", path, err)
	}
	if header.APIVersion == "" || header.Kind == "" {
		return Entry{}, fmt.Errorf("connection %s is not a Connection resource: it needs apiVersion: %s and kind: %s (a flat connection file must be rewritten as one)",
			path, crd.APIVersion, crd.Kind)
	}
	var resource crd.Connection
	if err := yaml.UnmarshalStrict(data, &resource); err != nil {
		return Entry{}, fmt.Errorf("decode connection %s: %w", path, err)
	}
	if resource.Name == "" {
		resource.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	connection, err := crd.ToModel(&resource)
	if err != nil {
		return Entry{}, fmt.Errorf("connection %s: %w", path, err)
	}
	connection.ID = connectionID(layer, connection)
	connection.UpdatedAt = info.ModTime()
	connection.CreatedAt = info.ModTime()
	connection.Source = "file://" + path
	connection.Virtual = true
	return Entry{Layer: layer, Path: path, Connection: connection}, nil
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
