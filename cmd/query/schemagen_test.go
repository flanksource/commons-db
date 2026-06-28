package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/commons-db/query/schema"
)

// TestGeneratedSchemasMatchCommitted fails when the committed schemas/ artifacts
// drift from the generators — i.e. someone changed query/schema but did not run
// `task query:schema`. The committed files are the source of truth the clicky-ui
// add/edit forms render from (served live with identical content).
func TestGeneratedSchemasMatchCommitted(t *testing.T) {
	for name, gen := range schemaFiles {
		want, err := schema.JSON(gen())
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		want = append(want, '\n')

		path := filepath.Join("..", "..", "schemas", name)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s (run `task query:schema`): %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale; run `task query:schema` to regenerate", path)
		}
	}
}

func TestWriteSchemasProducesBothFiles(t *testing.T) {
	dir := t.TempDir()
	if err := writeSchemas(dir); err != nil {
		t.Fatalf("writeSchemas: %v", err)
	}
	for name := range schemaFiles {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s to be written: %v", name, err)
		}
	}
}
