package commands

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/commons-db/cmd/query/internal/app"
	"github.com/spf13/cobra"
)

type Options struct {
	Args      []string
	Stdout    io.Writer
	Stderr    io.Writer
	BuildInfo BuildInfo
}

func New(options Options) (*cobra.Command, error) {
	if options.BuildInfo.Version == "" {
		return nil, fmt.Errorf("query build version is required")
	}
	if options.BuildInfo.Commit == "" {
		return nil, fmt.Errorf("query build commit is required")
	}
	if options.BuildInfo.Date == "" {
		return nil, fmt.Errorf("query build date is required")
	}
	root := &cobra.Command{
		Use:   "query",
		Short: "Connections, query profiles, and a web app to run them",
	}
	root.Version = options.BuildInfo.String()
	root.SetVersionTemplate("{{.Version}}\n")
	root.AddCommand(newVersion(options.BuildInfo))
	root.SetArgs(options.Args)
	root.SetOut(options.Stdout)
	root.SetErr(options.Stderr)
	root.PersistentFlags().String("config-dir", app.ResolveConfigDir(options.Args), "Query state directory (defaults to XDG config)")
	root.PersistentFlags().String("profiles-dir", app.ResolveProfilesDir(options.Args), "Directory of profile YAML files")
	// Version reporting must remain available when profile configuration is invalid.
	if requestsVersion(options.Args) {
		return root, nil
	}

	application, err := app.New(app.Options{
		Args: options.Args, Stdout: options.Stdout, Stderr: options.Stderr,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize query application: %w", err)
	}
	root.AddCommand(newServe(application), newSchema(), newTrace(application), newTop(application))

	if err := application.RegisterEntities(context.Background()); err != nil {
		return nil, err
	}
	clicky.GenerateCLI(root)
	return root, nil
}

func requestsVersion(args []string) bool {
	for index := 0; index < len(args); index++ {
		switch arg := args[index]; {
		case arg == "--version":
			return true
		case arg == "--config-dir" || arg == "--profiles-dir":
			index++
		case strings.HasPrefix(arg, "-"):
		default:
			return arg == "version"
		}
	}
	return false
}
