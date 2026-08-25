package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func newTestRuntime(t *testing.T, options DatabaseOptions) (*Runtime, *profiles.FileStore) {
	t.Helper()
	files, err := profiles.NewFileStore(t.TempDir())
	require.NoError(t, err)
	runtime, err := NewRuntime(dbcontext.NewContext(context.Background()), files, options)
	require.NoError(t, err)
	return runtime, files
}

func TestRuntimeWithoutDatabaseServesTheFileStore(t *testing.T) {
	runtime, files := newTestRuntime(t, DatabaseOptions{})

	store, err := runtime.ProfileStore()
	require.NoError(t, err)
	require.Same(t, files, store, "an empty --db must keep the YAML file store")

	_, err = runtime.Database()
	require.ErrorContains(t, err, "connections require a database")
	require.ErrorContains(t, err, "--db is empty")
	require.Empty(t, runtime.ConnectionString())
	require.NoError(t, runtime.Close(), "closing a runtime that never connected is a no-op")
}

func TestRuntimeDefersConnectingUntilTheStoreIsNeeded(t *testing.T) {
	// A data dir that was never written to proves construction alone does not
	// start PostgreSQL — `query --help` must stay instant.
	dataDir := filepath.Join(t.TempDir(), "postgres")
	runtime, _ := newTestRuntime(t, DatabaseOptions{URL: EmbeddedDatabase, DataDir: dataDir})

	require.True(t, runtime.dbOptions.Enabled())
	require.NoDirExists(t, dataDir)
}

func TestDatabaseOptionsEnabled(t *testing.T) {
	for _, test := range []struct {
		url     string
		enabled bool
	}{
		{url: "", enabled: false},
		{url: "   ", enabled: false},
		{url: EmbeddedDatabase, enabled: true},
		{url: "postgres://localhost:5432/query", enabled: true},
	} {
		require.Equal(t, test.enabled, DatabaseOptions{URL: test.url}.Enabled(), "url %q", test.url)
	}

	require.True(t, DatabaseOptions{URL: EmbeddedDatabase}.embedded())
	require.False(t, DatabaseOptions{URL: "postgres://localhost:5432/query"}.embedded())
}

func TestResolveDatabaseOptionsKeepsEmbeddedRunning(t *testing.T) {
	root := t.TempDir()
	unsetEnv(t, queryDBURLEnv)
	unsetEnv(t, queryDataDirEnv)

	options := ResolveDatabaseOptions([]string{"--config-dir", root})
	require.Equal(t, EmbeddedDatabase, options.URL)
	require.Equal(t, filepath.Join(root, "postgres"), options.DataDir)
	// A sub-command hands the cluster on to the next invocation instead of paying
	// another cold start; only `serve` stops what it started.
	require.True(t, options.KeepEmbeddedRunning)
}

func TestOpenDatabaseRejectsAnEmptyURL(t *testing.T) {
	_, err := openDatabase(context.Background(), DatabaseOptions{})
	require.ErrorContains(t, err, "database url is required")
}

func TestServeRejectsAnEmptyDatabase(t *testing.T) {
	application, err := New(Options{
		Args: []string{"--profiles-dir", t.TempDir()}, Stdout: &strings.Builder{}, Stderr: &strings.Builder{},
	})
	require.NoError(t, err)

	err = application.Serve(context.Background(), &cobra.Command{Use: "query"}, t.TempDir(), ServeOptions{})
	require.ErrorContains(t, err, "serve requires a database")
}
