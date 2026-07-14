package commands

import (
	"github.com/flanksource/commons-db/cmd/query/internal/app"
	"github.com/flanksource/commons-db/cmd/query/sessions"
	"github.com/spf13/cobra"
)

func newTrace(application *app.App) *cobra.Command {
	var options sessions.TraceOptions
	command := &cobra.Command{
		Use:   "trace <profile>",
		Short: "Run a trace profile: stream its events until it ends or Ctrl-C stops it",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return application.RunTrace(command.Context(), args[0], options)
		},
	}
	flags := command.Flags()
	flags.StringVar(&options.Duration, "duration", "", "Stop the trace after this long (default: the profile's maxDuration, capped at 15m)")
	flags.StringArrayVar(&options.Params, "param", nil, "Profile filter param as key=value (repeatable)")
	flags.StringVarP(&options.Output, "output", "o", "logfmt", "Event output format: logfmt or json")
	return command
}

func newTop(application *app.App) *cobra.Command {
	var options sessions.TopOptions
	command := &cobra.Command{
		Use:   "top <profile>",
		Short: "Sample any profile on an interval and render the latest snapshot as a live table",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return application.RunTop(command.Context(), args[0], options)
		},
	}
	flags := command.Flags()
	flags.StringVar(&options.Interval, "interval", "", "Sampling interval (default: the profile's top interval, or 5s)")
	flags.StringVar(&options.Duration, "duration", "", "Stop sampling after this long (default: the profile's maxDuration, capped at 15m)")
	flags.StringArrayVar(&options.Params, "param", nil, "Profile filter param as key=value (repeatable)")
	return command
}
