// Command query serves a clicky + clicky-ui app for managing connections and
// query profiles and executing them.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	// Register the built-in query providers and processors via their init().
	_ "github.com/flanksource/commons-db/query/processor"
	_ "github.com/flanksource/commons-db/query/providers"
)

func main() {
	root := &cobra.Command{
		Use:   "query",
		Short: "Connections, query profiles, and a web app to run them",
	}
	root.AddCommand(newServeCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
