package commands

import (
	"fmt"

	"github.com/flanksource/commons-db/cmd/query/internal/app"
	"github.com/spf13/cobra"
)

func newServe(application *app.App) *cobra.Command {
	options := app.DefaultServeOptions()
	command := &cobra.Command{
		Use:   "serve",
		Short: "Start the query web app (connections, profiles, execution)",
		RunE: func(command *cobra.Command, _ []string) error {
			persistent := command.Root().PersistentFlags()
			configDir, err := persistent.GetString("config-dir")
			if err != nil {
				return fmt.Errorf("read config-dir: %w", err)
			}
			if options.DatabaseURL, err = persistent.GetString("db"); err != nil {
				return fmt.Errorf("read db: %w", err)
			}
			if options.DataDir, err = persistent.GetString("data-dir"); err != nil {
				return fmt.Errorf("read data-dir: %w", err)
			}
			return application.Serve(command.Context(), command.Root(), configDir, options)
		},
	}
	flags := command.Flags()
	flags.StringVar(&options.Host, "host", options.Host, "Host to bind")
	flags.IntVarP(&options.Port, "port", "p", options.Port, "Port to bind")
	flags.BoolVar(&options.Dev, "dev", options.Dev, "Spawn a Vite dev server and proxy the UI to it")
	flags.IntVar(&options.MaxSessions, "max-sessions", options.MaxSessions, "Maximum concurrently running trace/top sessions")
	flags.DurationVar(&options.MaxSessionDuration, "max-session-duration", options.MaxSessionDuration, "Upper bound on any trace/top session; profiles can only lower it")
	flags.DurationVar(&options.SessionRetention, "session-retention", options.SessionRetention, "How long finished sessions and their events are kept in PostgreSQL")
	return command
}
