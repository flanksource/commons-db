package commands

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/flanksource/commons-db/cmd/query/connections"
	connectioncli "github.com/flanksource/commons-db/cmd/query/connections/cli"
	"github.com/flanksource/commons-db/cmd/query/internal/app"
	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/connection/store"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/spf13/cobra"
)

func newConnections(args []string) *cobra.Command {
	return connectioncli.New(connectioncli.Options{Open: func(source string) (*connections.Service, func() error, error) {
		close := func() error { return nil }
		ctx := dbcontext.New()
		options := connections.Options{
			Context: func() dbcontext.Context { return ctx }, DecodeBody: app.DecodeBody,
			Profiles: func(context.Context) ([]query.Profile, error) { return nil, nil },
		}
		if source == "file" {
			layers, err := store.DefaultLayers("query")
			if err != nil {
				return nil, close, err
			}
			layers[1].Dir = filepath.Join(app.ResolveConfigDir(args), "connections")
			files, err := store.New(layers...)
			if err != nil {
				return nil, close, err
			}
			provider, err := connections.NewStoreProvider(connections.StoreProviderOptions{Store: files, WriteLayer: store.LayerGlobal, WritableLayers: []string{store.LayerGlobal, store.LayerApp, store.LayerCWD}})
			if err != nil {
				return nil, close, err
			}
			options.Virtual = provider
			ctx = files.Attach(ctx)
		} else {
			fileStore, err := profiles.NewFileStore(filepath.Join(app.ResolveConfigDir(args), "profiles"))
			if err != nil {
				return nil, close, err
			}
			runtime, err := app.NewRuntime(ctx, fileStore, app.ResolveDatabaseOptions(args))
			if err != nil {
				return nil, close, err
			}
			options.Database = runtime.Database
			options.Context = runtime.Context
			close = runtime.Close
		}
		service, err := connections.New(options)
		return service, close, err
	}})
}

func requestsConnections(args []string) bool {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if flagsWithValues[arg] {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if arg == "help" {
			continue
		}
		return arg == "connections"
	}
	return false
}
