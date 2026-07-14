package app

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
	for name, doc := range generatedSchemas() {
		want, err := schema.JSON(doc)
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		want = append(want, '\n')

		path := filepath.Join("..", "..", "..", "..", "schemas", name)
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
	stale := filepath.Join(dir, "src", "connections", "stale.json")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteSchemas(dir); err != nil {
		t.Fatalf("WriteSchemas: %v", err)
	}
	for name := range generatedSchemas() {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("expected %s to be written: %v", name, err)
		}
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale generated component was not removed: %v", err)
	}
}
