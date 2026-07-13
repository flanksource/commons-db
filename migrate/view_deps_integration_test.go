package migrate

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	commonsdb "github.com/flanksource/commons-db/db"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// viewDepsTableHCL declares a table `t(id text, c <ctype>)` in the public schema.
func viewDepsTableHCL(ctype string) string {
	return `
schema "public" {}
table "t" {
  schema = schema.public
  column "id" {
    null = false
    type = text
  }
  column "c" {
    null = true
    type = ` + ctype + `
  }
}
`
}

func startViewDepsDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	if os.Getenv("COMMONS_DB_EMBEDDED_TEST") == "" {
		t.Skip("set COMMONS_DB_EMBEDDED_TEST=1 to run embedded-postgres integration tests")
	}
	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "view_deps_runner",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return dsn, db
}

func viewExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM information_schema.views WHERE table_schema='public' AND table_name=$1`, name).Scan(&count))
	return count == 1
}

// TestApplyInvalidatesDependentViews proves that a column change blocked by a managed view
// succeeds: the view is dropped, its (content-unchanged) script is invalidated so the post
// phase recreates it. Without the fix Atlas fails with "cannot alter … other objects depend
// on it".
func TestApplyInvalidatesDependentViews(t *testing.T) {
	dsn, db := startViewDepsDB(t)
	ctx := t.Context()

	fs := fstest.MapFS{
		"migrations/schema.hcl": {Data: []byte(viewDepsTableHCL("bigint"))},
		"migrations/view.sql":   {Data: []byte("CREATE OR REPLACE VIEW public.v AS SELECT id, c FROM public.t")},
	}
	require.NoError(t, Apply(ctx, dsn, fs, WithDir("migrations"), WithName("views")))
	require.True(t, viewExists(t, db, "v"))

	// Retype c bigint->text; the view.sql content is deliberately unchanged so only the
	// hash-invalidation path can force it to re-run.
	fs["migrations/schema.hcl"] = &fstest.MapFile{Data: []byte(viewDepsTableHCL("text"))}
	require.NoError(t, Apply(ctx, dsn, fs, WithDir("migrations"), WithName("views")))

	var dataType string
	require.NoError(t, db.QueryRow(
		`SELECT data_type FROM information_schema.columns WHERE table_name='t' AND column_name='c'`).Scan(&dataType))
	assert.Equal(t, "text", dataType)
	assert.True(t, viewExists(t, db, "v"), "managed view should be recreated by the post phase")

	var recorded int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM schema_migration_scripts WHERE scope='views' AND path='view.sql'`).Scan(&recorded))
	assert.Equal(t, 1, recorded, "invalidated view script should be re-recorded after rerun")
}

// TestApplyRejectsUnmanagedDependentView proves the fail-loud guard: when an un-owned view
// depends on a column being altered, apply aborts naming it and leaves every view intact.
func TestApplyRejectsUnmanagedDependentView(t *testing.T) {
	dsn, db := startViewDepsDB(t)
	ctx := t.Context()

	fs := fstest.MapFS{
		"migrations/schema.hcl": {Data: []byte(viewDepsTableHCL("bigint"))},
		"migrations/view.sql":   {Data: []byte("CREATE OR REPLACE VIEW public.v AS SELECT id, c FROM public.t")},
	}
	require.NoError(t, Apply(ctx, dsn, fs, WithDir("migrations"), WithName("views")))

	_, err := db.ExecContext(ctx, "CREATE VIEW public.ext AS SELECT c FROM public.t")
	require.NoError(t, err)

	fs["migrations/schema.hcl"] = &fstest.MapFile{Data: []byte(viewDepsTableHCL("text"))}
	err = Apply(ctx, dsn, fs, WithDir("migrations"), WithName("views"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "public.ext")

	assert.True(t, viewExists(t, db, "v"), "managed view must not be dropped when the guard aborts")
	assert.True(t, viewExists(t, db, "ext"), "unmanaged view must be left untouched")
}
