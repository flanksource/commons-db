package migrate

import (
	"database/sql"
	"testing"
	"testing/fstest"

	"github.com/flanksource/commons-db/dbtest"
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

// startViewDepsDB gives one test its own database, so the unqualified
// schema_migration_* metadata tables cannot collide between tests.
func startViewDepsDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	handle := dbtest.ForT(t, dbtest.Options{Name: "view_deps_runner"})
	return handle.DSN(), handle.SQL()
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

// TestApplyRestoresUnmanagedDependentView proves that a view no migration owns does not
// block a schema change and is not lost to it: apply captures its definition, drops it,
// applies the change, and recreates it from the capture.
func TestApplyRestoresUnmanagedDependentView(t *testing.T) {
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
	require.NoError(t, Apply(ctx, dsn, fs, WithDir("migrations"), WithName("views")))

	var dataType string
	require.NoError(t, db.QueryRow(
		`SELECT data_type FROM information_schema.columns WHERE table_name='t' AND column_name='c'`).Scan(&dataType))
	assert.Equal(t, "text", dataType)
	assert.True(t, viewExists(t, db, "v"), "managed view should be recreated by the post phase")
	assert.True(t, viewExists(t, db, "ext"), "unmanaged view should be restored from its capture")
}

// TestApplyReportsUnrestorableView proves the loud-failure path: when the schema change makes
// an unmanaged view impossible to recreate, apply fails naming the view and printing its DDL.
func TestApplyReportsUnrestorableView(t *testing.T) {
	dsn, db := startViewDepsDB(t)
	ctx := t.Context()

	fs := fstest.MapFS{
		"migrations/schema.hcl": {Data: []byte(viewDepsTableHCL("bigint"))},
	}
	require.NoError(t, Apply(ctx, dsn, fs, WithDir("migrations"), WithName("views")))

	// ext reads column c, which the next apply drops entirely.
	_, err := db.ExecContext(ctx, "CREATE VIEW public.ext AS SELECT c FROM public.t")
	require.NoError(t, err)

	fs["migrations/schema.hcl"] = &fstest.MapFile{Data: []byte(`
schema "public" {}
table "t" {
  schema = schema.public
  column "id" {
    null = false
    type = text
  }
}
`)}
	err = Apply(ctx, dsn, fs, WithDir("migrations"), WithName("views"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "public.ext")
	assert.Contains(t, err.Error(), "CREATE VIEW", "error must carry replayable DDL")
}
