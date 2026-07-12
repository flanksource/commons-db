package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/query/schema"
	"github.com/spf13/cobra"
)

func generatedSchemas() map[string]schema.Schema {
	files := map[string]schema.Schema{
		"connection.json":     schema.Connection(),
		"profile.json":        schema.Profile(),
		"src/connection.json": schema.ConnectionSource(),
		"src/profile.json":    schema.ProfileSource(),
	}
	for name, component := range schema.ConnectionComponents() {
		files[filepath.Join("src", "connections", name+".json")] = component
	}
	for name, component := range schema.ProfileComponents() {
		files[filepath.Join("src", "profiles", name+".json")] = component
	}
	return files
}

// newSchemaCmd writes the connection and profile JSON Schemas to --out.
func newSchemaCmd() *cobra.Command {
	out := "schemas"
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Generate source and bundled connection/profile JSON Schemas",
		RunE: func(_ *cobra.Command, _ []string) error {
			return writeSchemas(out)
		},
	}
	cmd.Flags().StringVar(&out, "out", out, "Output directory for the generated schema files")
	return cmd
}

// writeSchemas renders each schema to <dir>/<file>, creating the directory.
func writeSchemas(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create schema dir %q: %w", dir, err)
	}
	// src is entirely generated. Recreate it so removed connection/provider
	// types cannot leave stale component files behind.
	if err := os.RemoveAll(filepath.Join(dir, "src")); err != nil {
		return fmt.Errorf("clean generated schema components: %w", err)
	}
	for name, doc := range generatedSchemas() {
		body, err := schema.JSON(doc)
		if err != nil {
			return fmt.Errorf("render %s: %w", name, err)
		}
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create schema directory for %s: %w", path, err)
		}
		if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Fprintf(os.Stderr, "📝 wrote %s\n", path)
	}
	return nil
}
