package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"testing/fstest"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/lib/pq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyScriptsAndSecurity(t *testing.T) {
	handle := dbtest.ForT(t, dbtest.Options{Name: "migrate_runner"})
	dsn, db := handle.DSN(), handle.SQL()
	roles := rolesFor(handle.Unique())
	// Registered after the database itself, so LIFO cleanup revokes the roles'
	// grants while the database still exists and only then drops the database.
	t.Cleanup(func() { dropRoles(t, db, roles) })

	filesystem := migrationTestFS(migrationSecurityHCL(true, roles), "pre-v1")
	require.NoError(t, Apply(t.Context(), dsn, filesystem, WithDir("migrations"), WithName("integration")))
	assertEvents(t, db, []string{"pre-v1", "post"})
	assertSecurity(t, db, roles, true)

	// Unchanged scripts do not execute again.
	require.NoError(t, Apply(t.Context(), dsn, filesystem, WithDir("migrations"), WithName("integration")))
	assertEvents(t, db, []string{"pre-v1", "post"})

	// A changed pre script invalidates its transitive post dependent.
	filesystem["migrations/001_pre.sql"] = &fstest.MapFile{Data: []byte(preSQL("pre-v2"))}
	require.NoError(t, Apply(t.Context(), dsn, filesystem, WithDir("migrations"), WithName("integration")))
	assertEvents(t, db, []string{"pre-v1", "post", "pre-v2", "post"})

	// Drift within a declared pair is revoked; unrelated grants are preserved.
	_, err := db.Exec(fmt.Sprintf(`GRANT DELETE ON TABLE public.secured_items TO %[1]s;
CREATE ROLE %[2]s;
GRANT SELECT ON TABLE public.secured_items TO %[2]s`,
		pq.QuoteIdentifier(roles.reader), pq.QuoteIdentifier(roles.outsider)))
	require.NoError(t, err)
	require.NoError(t, Apply(t.Context(), dsn, filesystem, WithDir("migrations"), WithName("integration")))
	assertPrivilege(t, db, fmt.Sprintf(`SELECT has_table_privilege('%s', 'public.secured_items', 'DELETE')`, roles.reader), false)
	assertPrivilege(t, db, fmt.Sprintf(`SELECT has_table_privilege('%s', 'public.secured_items', 'SELECT')`, roles.outsider), true)

	// Removing managed grants and membership revokes them without dropping roles.
	filesystem["migrations/schema.hcl"] = &fstest.MapFile{Data: []byte(migrationSecurityHCL(false, roles))}
	require.NoError(t, Apply(t.Context(), dsn, filesystem, WithDir("migrations"), WithName("integration")))
	assertSecurity(t, db, roles, false)
	var roleExists bool
	require.NoError(t, db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, roles.reader).Scan(&roleExists))
	assert.True(t, roleExists)

	// Always-run scripts execute on every apply.
	alwaysFS := fstest.MapFS{
		"schema.hcl": &fstest.MapFile{Data: []byte(migrationSecurityHCL(false, roles))},
		"always.sql": &fstest.MapFile{Data: []byte("-- runs: always\nINSERT INTO public.runner_events(value) VALUES ('always')")},
	}
	require.NoError(t, Apply(t.Context(), dsn, alwaysFS, WithName("always")))
	require.NoError(t, Apply(t.Context(), dsn, alwaysFS, WithName("always")))
	var alwaysCount int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM public.runner_events WHERE value = 'always'`).Scan(&alwaysCount))
	assert.Equal(t, 2, alwaysCount)

	// A transactional script failure rolls back its SQL and does not record a hash.
	badFS := fstest.MapFS{
		"schema.hcl": &fstest.MapFile{Data: []byte(migrationSecurityHCL(false, roles))},
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

// roles names the cluster-global roles this test creates. PostgreSQL roles live
// outside any database, so a per-run suffix — not the scratch database — is what
// keeps concurrent runs against one server from colliding.
type roles struct{ parent, reader, outsider string }

func rolesFor(unique string) roles {
	return roles{
		parent:   "migration_parent_" + unique,
		reader:   "migration_reader_" + unique,
		outsider: "migration_outsider_" + unique,
	}
}

// dropRoles revokes and removes the roles the test created. DROP OWNED BY clears
// the grants they hold in the current database, without which DROP ROLE fails.
func dropRoles(t *testing.T, db *sql.DB, r roles) {
	t.Helper()
	for _, role := range []string{r.outsider, r.reader, r.parent} {
		var exists bool
		if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
			t.Errorf("check role %s: %v", role, err)
			continue
		}
		if !exists {
			continue
		}
		quoted := pq.QuoteIdentifier(role)
		if _, err := db.Exec(`DROP OWNED BY ` + quoted); err != nil {
			t.Errorf("drop owned by %s: %v", role, err)
			continue
		}
		if _, err := db.Exec(`DROP ROLE ` + quoted); err != nil {
			t.Errorf("drop role %s: %v", role, err)
		}
	}
}

func migrationSecurityHCL(withGrants bool, r roles) string {
	base := `
schema "public" {}
table "secured_items" {
  schema = schema.public
  column "id" { type = text }
}
role %[1]q {}
role "pg_read_all_data" { external = true }
role %[2]q {
  comment = "migration integration reader"
  member_of = %[3]s
}
`
	membership := "[]"
	permissions := ""
	if withGrants {
		membership = "[role." + r.parent + "]"
		permissions = fmt.Sprintf(`
permission {
  to = role.%[1]s
  for = schema.public
  privileges = [USAGE]
}
permission {
  to = role.%[1]s
  for = table.secured_items
  privileges = [SELECT]
}
permission {
  to = role.%[1]s
  for = table.secured_items.column.id
  privileges = [UPDATE]
}
permission {
  to = role.%[1]s
  for = "sequence:public.migration_sequence"
  privileges = [USAGE]
}
`, r.reader)
	}
	return fmt.Sprintf(base, r.parent, r.reader, membership) + permissions
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

func assertSecurity(t *testing.T, db *sql.DB, r roles, enabled bool) {
	t.Helper()
	assertPrivilege(t, db, fmt.Sprintf(`SELECT pg_has_role('%s', '%s', 'MEMBER')`, r.reader, r.parent), enabled)
	assertPrivilege(t, db, fmt.Sprintf(`SELECT EXISTS (
  SELECT 1
  FROM pg_namespace n
  CROSS JOIN LATERAL aclexplode(COALESCE(n.nspacl, acldefault('n', n.nspowner))) x
  JOIN pg_roles r ON r.oid = x.grantee
  WHERE n.nspname = 'public' AND r.rolname = '%s' AND x.privilege_type = 'USAGE'
)`, r.reader), enabled)
	assertPrivilege(t, db, fmt.Sprintf(`SELECT has_table_privilege('%s', 'public.secured_items', 'SELECT')`, r.reader), enabled)
	assertPrivilege(t, db, fmt.Sprintf(`SELECT has_column_privilege('%s', 'public.secured_items', 'id', 'UPDATE')`, r.reader), enabled)
	assertPrivilege(t, db, fmt.Sprintf(`SELECT has_sequence_privilege('%s', 'public.migration_sequence', 'USAGE')`, r.reader), enabled)
}

func assertPrivilege(t *testing.T, db *sql.DB, query string, want bool) {
	t.Helper()
	var got bool
	require.NoError(t, db.QueryRowContext(context.Background(), query).Scan(&got))
	assert.Equal(t, want, got, query)
}
