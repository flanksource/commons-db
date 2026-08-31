package migrate

import (
	"database/sql"
	"testing"
	"testing/fstest"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// coTenantHCL declares only the bundle's own table. The enum and table seeded by
// the co-tenant below are deliberately absent, which is what makes the realm diff
// want to drop them.
const coTenantHCL = `
schema "public" {}
table "owned" {
  schema = schema.public
  column "id" {
    null = false
    type = text
  }
}
`

func typeExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM pg_catalog.pg_type WHERE typname = $1`, name).Scan(&count))
	return count > 0
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name=$1`,
		name).Scan(&count))
	return count == 1
}

// TestApplyKeepsUndeclaredEnumsUsedByKeptTables proves a partial schema bundle
// cannot destroy a co-tenant's enum. Captain and its host share one public
// schema, so each Apply inspects objects the other owns; the diff wants to drop
// all of them, and drop suppression keeps the table. Suppressing only the table
// left the DROP TYPE in the plan, and it failed against the column it had just
// preserved:
//
//	drop enum type "captain_git_agent_task_status": pq: cannot drop type
//	captain_git_agent_task_status because other objects depend on it
//
// which surfaced as a store-open failure for any binary carrying an older bundle.
func TestApplyKeepsUndeclaredEnumsUsedByKeptTables(t *testing.T) {
	handle := dbtest.ForT(t, dbtest.Options{Name: "enum_drop_runner"})
	dsn, db := handle.DSN(), handle.SQL()
	ctx := t.Context()

	_, err := db.ExecContext(ctx,
		`CREATE TYPE public.co_tenant_status AS ENUM ('dispatched', 'running', 'errored')`)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE TABLE public.co_tenant_tasks (
		id text PRIMARY KEY,
		status public.co_tenant_status NOT NULL DEFAULT 'dispatched'
	)`)
	require.NoError(t, err)

	fs := fstest.MapFS{"migrations/schema.hcl": {Data: []byte(coTenantHCL)}}
	require.NoError(t, Apply(ctx, dsn, fs, WithDir("migrations"), WithName("co-tenant")))

	assert.True(t, tableExists(t, db, "owned"), "the bundle's own table should be created")
	assert.True(t, typeExists(t, db, "co_tenant_status"), "co-tenant enum should survive")
	assert.True(t, tableExists(t, db, "co_tenant_tasks"), "co-tenant table should survive")

	// Re-applying is the path a second process takes against the same database.
	require.NoError(t, Apply(ctx, dsn, fs, WithDir("migrations"), WithName("co-tenant")))
	assert.True(t, typeExists(t, db, "co_tenant_status"))

	// Writing through the column proves it still resolves its enum type, not just
	// that a row in pg_type survived.
	_, err = db.ExecContext(ctx,
		`INSERT INTO public.co_tenant_tasks (id, status) VALUES ('t1', 'running')`)
	require.NoError(t, err)

	var status string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT status FROM public.co_tenant_tasks WHERE id = 't1'`).Scan(&status))
	assert.Equal(t, "running", status)
}
