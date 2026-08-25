package main

import (
	"fmt"
	"os"

	"github.com/flanksource/commons-db/cmd/query/internal/commands"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	root, err := commands.New(commands.Options{
		Args: os.Args[1:], Stdout: os.Stdout, Stderr: os.Stderr,
		BuildInfo: commands.BuildInfo{Version: version, Commit: commit, Date: date},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
