package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	commonsdb "github.com/flanksource/commons-db/db"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyScriptsAndSecurity(t *testing.T) {
	if os.Getenv("COMMONS_DB_EMBEDDED_TEST") == "" {
		t.Skip("set COMMONS_DB_EMBEDDED_TEST=1 to run embedded-postgres integration tests")
	}
	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "migrate_runner",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	filesystem := migrationTestFS(migrationSecurityHCL(true), "pre-v1")
	require.NoError(t, Apply(t.Context(), dsn, filesystem, WithDir("migrations"), WithName("integration")))
	assertEvents(t, db, []string{"pre-v1", "post"})
	assertSecurity(t, db, true)

	// Unchanged scripts do not execute again.
	require.NoError(t, Apply(t.Context(), dsn, filesystem, WithDir("migrations"), WithName("integration")))
	assertEvents(t, db, []string{"pre-v1", "post"})

	// A changed pre script invalidates its transitive post dependent.
	filesystem["migrations/001_pre.sql"] = &fstest.MapFile{Data: []byte(preSQL("pre-v2"))}
	require.NoError(t, Apply(t.Context(), dsn, filesystem, WithDir("migrations"), WithName("integration")))
	assertEvents(t, db, []string{"pre-v1", "post", "pre-v2", "post"})

	// Drift within a declared pair is revoked; unrelated grants are preserved.
	_, err = db.Exec(`GRANT DELETE ON TABLE public.secured_items TO migration_reader;
CREATE ROLE migration_outsider;
GRANT SELECT ON TABLE public.secured_items TO migration_outsider`)
	require.NoError(t, err)
	require.NoError(t, Apply(t.Context(), dsn, filesystem, WithDir("migrations"), WithName("integration")))
	assertPrivilege(t, db, `SELECT has_table_privilege('migration_reader', 'public.secured_items', 'DELETE')`, false)
	assertPrivilege(t, db, `SELECT has_table_privilege('migration_outsider', 'public.secured_items', 'SELECT')`, true)

	// Removing managed grants and membership revokes them without dropping roles.
	filesystem["migrations/schema.hcl"] = &fstest.MapFile{Data: []byte(migrationSecurityHCL(false))}
	require.NoError(t, Apply(t.Context(), dsn, filesystem, WithDir("migrations"), WithName("integration")))
	assertSecurity(t, db, false)
	var roleExists bool
	require.NoError(t, db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'migration_reader')`).Scan(&roleExists))
	assert.True(t, roleExists)

	// Always-run scripts execute on every apply.
	alwaysFS := fstest.MapFS{
		"schema.hcl": &fstest.MapFile{Data: []byte(migrationSecurityHCL(false))},
		"always.sql": &fstest.MapFile{Data: []byte("-- runs: always\nINSERT INTO public.runner_events(value) VALUES ('always')")},
	}
	require.NoError(t, Apply(t.Context(), dsn, alwaysFS, WithName("always")))
	require.NoError(t, Apply(t.Context(), dsn, alwaysFS, WithName("always")))
	var alwaysCount int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM public.runner_events WHERE value = 'always'`).Scan(&alwaysCount))
	assert.Equal(t, 2, alwaysCount)

	// A transactional script failure rolls back its SQL and does not record a hash.
	badFS := fstest.MapFS{
		"schema.hcl": &fstest.MapFile{Data: []byte(migrationSecurityHCL(false))},
		"bad.sql": &fstest.MapFile{Data: []byte(`-- phase: pre
CREATE TABLE public.must_rollback (id integer);
INSERT INTO public.missing_table VALUES (1)`)},
	}
	require.Error(t, Apply(t.Context(), dsn, badFS, WithName("rollback")))
	var rolledBack *string
	require.NoError(t, db.QueryRow(`SELECT to_regclass('public.must_rollback')::text`).Scan(&rolledBack))
	assert.Nil(t, rolledBack)
	var logged int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM schema_migration_scripts WHERE scope = 'rollback'`).Scan(&logged))
	assert.Zero(t, logged)
}

func migrationTestFS(hcl, preValue string) fstest.MapFS {
	return fstest.MapFS{
		"migrations/schema.hcl":  &fstest.MapFile{Data: []byte(hcl)},
		"migrations/001_pre.sql": &fstest.MapFile{Data: []byte(preSQL(preValue))},
		"migrations/002_post.sql": &fstest.MapFile{Data: []byte(`-- dependsOn: 001_pre.sql
CREATE SEQUENCE IF NOT EXISTS public.migration_sequence;
INSERT INTO public.runner_events(value) VALUES ('post')`)},
	}
}

func preSQL(value string) string {
	return `-- phase: pre
CREATE TABLE IF NOT EXISTS public.runner_events (
  id bigserial PRIMARY KEY,
  value text NOT NULL
);
INSERT INTO public.runner_events(value) VALUES ('` + value + `')`
}

func migrationSecurityHCL(withGrants bool) string {
	base := `
schema "public" {}
table "secured_items" {
  schema = schema.public
  column "id" { type = text }
}
role "migration_parent" {}
role "pg_read_all_data" { external = true }
role "migration_reader" {
  comment = "migration integration reader"
  member_of = %s
}
`
	membership := "[]"
	permissions := ""
	if withGrants {
		membership = "[role.migration_parent]"
		permissions = `
permission {
  to = role.migration_reader
  for = schema.public
  privileges = [USAGE]
}
permission {
  to = role.migration_reader
  for = table.secured_items
  privileges = [SELECT]
}
permission {
  to = role.migration_reader
  for = table.secured_items.column.id
  privileges = [UPDATE]
}
permission {
  to = role.migration_reader
  for = "sequence:public.migration_sequence"
  privileges = [USAGE]
}
`
	}
	return fmt.Sprintf(base, membership) + permissions
}

func assertEvents(t *testing.T, db *sql.DB, want []string) {
	t.Helper()
	rows, err := db.Query(`SELECT value FROM public.runner_events ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var got []string
	for rows.Next() {
		var value string
		require.NoError(t, rows.Scan(&value))
		got = append(got, value)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, want, got)
}

func assertSecurity(t *testing.T, db *sql.DB, enabled bool) {
	t.Helper()
	assertPrivilege(t, db, `SELECT pg_has_role('migration_reader', 'migration_parent', 'MEMBER')`, enabled)
	assertPrivilege(t, db, `SELECT EXISTS (
  SELECT 1
  FROM pg_namespace n
  CROSS JOIN LATERAL aclexplode(COALESCE(n.nspacl, acldefault('n', n.nspowner))) x
  JOIN pg_roles r ON r.oid = x.grantee
  WHERE n.nspname = 'public' AND r.rolname = 'migration_reader' AND x.privilege_type = 'USAGE'
)`, enabled)
	assertPrivilege(t, db, `SELECT has_table_privilege('migration_reader', 'public.secured_items', 'SELECT')`, enabled)
	assertPrivilege(t, db, `SELECT has_column_privilege('migration_reader', 'public.secured_items', 'id', 'UPDATE')`, enabled)
	assertPrivilege(t, db, `SELECT has_sequence_privilege('migration_reader', 'public.migration_sequence', 'USAGE')`, enabled)
}

func assertPrivilege(t *testing.T, db *sql.DB, query string, want bool) {
	t.Helper()
	var got bool
	require.NoError(t, db.QueryRowContext(context.Background(), query).Scan(&got))
	assert.Equal(t, want, got, query)
}
