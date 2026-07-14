package commands

import (
	"context"
	"fmt"
	"io"

	"github.com/flanksource/clicky"
	"github.com/flanksource/commons-db/cmd/query/internal/app"
	"github.com/spf13/cobra"
)

type Options struct {
	Args   []string
	Stdout io.Writer
	Stderr io.Writer
}

func New(options Options) (*cobra.Command, error) {
	application, err := app.New(app.Options{
		Args: options.Args, Stdout: options.Stdout, Stderr: options.Stderr,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize query application: %w", err)
	}
	root := &cobra.Command{
		Use:   "query",
		Short: "Connections, query profiles, and a web app to run them",
	}
	root.SetArgs(options.Args)
	root.SetOut(options.Stdout)
	root.SetErr(options.Stderr)
	root.PersistentFlags().String("config-dir", app.ResolveConfigDir(options.Args), "Query state directory (defaults to XDG config)")
	root.PersistentFlags().String("profiles-dir", app.ResolveProfilesDir(options.Args), "Directory of profile YAML files")
	root.AddCommand(newServe(application), newSchema(), newTrace(application), newTop(application))

	if err := application.RegisterEntities(context.Background()); err != nil {
		return nil, err
	}
	clicky.GenerateCLI(root)
	return root, nil
}
