package app

import (
	"context"
	"testing"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/stretchr/testify/require"
)

// A DSN given to a sub-command has to bring the schema with it, otherwise
// `query --db postgres://…` against a fresh database fails on a missing table.
func TestRuntimeConnectsMigratesAndServesTheDatabaseStore(t *testing.T) {
	handle := dbtest.ForT(t, dbtest.Options{Name: "query_runtime_db", LogName: "query-runtime-db-test"})
	files, err := profiles.NewFileStore(t.TempDir())
	require.NoError(t, err)
	runtime, err := NewRuntime(dbcontext.NewContext(context.Background()), files, DatabaseOptions{URL: handle.DSN()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = runtime.Close() })

	store, err := runtime.ProfileStore()
	require.NoError(t, err)
	require.IsType(t, &profiles.DBStore{}, store, "a --db DSN must swap in the database-backed store")
	require.Equal(t, handle.DSN(), runtime.ConnectionString())

	gdb, err := runtime.Database()
	require.NoError(t, err)
	require.NoError(t, gdb.Create(&models.Connection{Name: "runtime-db", Type: models.ConnectionTypePostgres}).Error)

	// Connecting is once-only: a second caller reuses the same handles.
	require.NoError(t, runtime.EnsureDatabase(context.Background()))
	again, err := runtime.ProfileStore()
	require.NoError(t, err)
	require.Same(t, store, again)

	require.NoError(t, store.Save(context.Background(), query.Profile{
		Name: "Runtime", Provider: query.ProviderConfig{Type: "sql", Connection: "connection://runtime-db"},
		Query: "select 1",
	}))
	got, err := store.Get(context.Background(), "Runtime")
	require.NoError(t, err)
	require.Equal(t, "select 1", got.Query)

	// The context the providers execute against carries the same database.
	require.NotNil(t, runtime.Context().DB())
}
