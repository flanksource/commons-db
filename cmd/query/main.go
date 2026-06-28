// Command query serves a clicky + clicky-ui app for managing connections and
// query profiles and executing them, and exposes the same connections, profiles
// and per-profile surfaces as cobra subcommands.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/spf13/cobra"

	// Register the built-in query providers and processors via their init().
	_ "github.com/flanksource/commons-db/query/processor"
	_ "github.com/flanksource/commons-db/query/providers"
)

// defaultProfilesDir is the profile YAML directory used when --profiles-dir and
// QUERY_PROFILES_DIR are both unset.
const defaultProfilesDir = "./profiles"

func main() {
	root := &cobra.Command{
		Use:   "query",
		Short: "Connections, query profiles, and a web app to run them",
	}
	root.PersistentFlags().String("profiles-dir", defaultProfilesDir, "Directory of profile YAML files")
	root.AddCommand(newServeCmd())
	root.AddCommand(newSchemaCmd())

	if err := shadowInit(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// shadowInit resolves the profiles directory ahead of cobra's flag parsing and
// registers the connection, profile, and per-profile entities so they materialize
// as real cobra subcommands (and on the HTTP executor) before root.Execute. The
// DB-backed execution context and database are injected later by runServe; the
// base CLI (profile reads, base profile execution) needs neither.
func shadowInit(root *cobra.Command) error {
	store, err := NewProfileStore(resolveProfilesDir(os.Args[1:]))
	if err != nil {
		return err
	}
	setStore(store)
	fmt.Fprintf(os.Stderr, "📁 profiles loaded from %s\n", store.Dir)

	registerConnectionEntity()
	registerProfileEntity(store)
	if err := registerProfileEntities(store); err != nil {
		return fmt.Errorf("register profile entities: %w", err)
	}
	clicky.GenerateCLI(root)
	return nil
}

// resolveProfilesDir reads the profiles directory the way the persistent flag
// will, but before cobra parses: --profiles-dir <dir> / --profiles-dir=<dir>,
// then QUERY_PROFILES_DIR, then the default.
func resolveProfilesDir(args []string) string {
	const flag = "--profiles-dir"
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v
		}
	}
	if v := os.Getenv("QUERY_PROFILES_DIR"); v != "" {
		return v
	}
	return defaultProfilesDir
}
