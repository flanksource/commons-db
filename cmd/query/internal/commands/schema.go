package commands

import (
	"github.com/flanksource/commons-db/cmd/query/internal/app"
	"github.com/spf13/cobra"
)

func newSchema() *cobra.Command {
	out := "schemas"
	command := &cobra.Command{
		Use:   "schema",
		Short: "Generate source and bundled connection/profile JSON Schemas",
		RunE: func(_ *cobra.Command, _ []string) error {
			return app.WriteSchemas(out)
		},
	}
	command.Flags().StringVar(&out, "out", out, "Output directory for the generated schema files")
	return command
}
