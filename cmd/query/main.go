package main

import (
	"fmt"
	"os"

	"github.com/flanksource/commons-db/cmd/query/internal/commands"
	"github.com/flanksource/commons-db/connection/awssdk"
	"github.com/flanksource/commons-db/connection/azuresdk"
	_ "github.com/flanksource/commons/logger/yamlcolor"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	// query is an interactive CLI: keep AWS profiles/SSO/credential_process and
	// Azure CLI sign-in working, which the SDK-free built-in chains omit.
	awssdk.Install()
	azuresdk.Install()
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
