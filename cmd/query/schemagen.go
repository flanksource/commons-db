package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/query/schema"
	"github.com/spf13/cobra"
)

// schemaFiles maps an output filename to the schema generator producing it. These
// are the canonical connection/profile form schemas that drive the clicky-ui
// add/edit forms; the same documents are served live via content negotiation.
var schemaFiles = map[string]func() schema.Schema{
	"connection.json": schema.Connection,
	"profile.json":    schema.Profile,
}

// newSchemaCmd writes the connection and profile JSON Schemas to --out.
func newSchemaCmd() *cobra.Command {
	out := "schemas"
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Generate connection/profile JSON Schemas into a directory",
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
	for name, gen := range schemaFiles {
		body, err := schema.JSON(gen())
		if err != nil {
			return fmt.Errorf("render %s: %w", name, err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Fprintf(os.Stderr, "📝 wrote %s\n", path)
	}
	return nil
}
