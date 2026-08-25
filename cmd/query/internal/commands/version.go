package commands

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

func (info BuildInfo) String() string {
	return fmt.Sprintf(
		"query version %s (commit: %s, built: %s, go: %s)",
		info.Version, info.Commit, info.Date, runtime.Version(),
	)
}

func newVersion(info BuildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		Run: func(command *cobra.Command, _ []string) {
			fmt.Fprintln(command.OutOrStdout(), info.String())
		},
	}
}
