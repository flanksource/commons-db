package migrate

import (
	"database/sql"
	"fmt"
	"testing"
	"testing/fstest"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	checkTable = "check_items"
	checkName  = "chk_check_items_state"
)

// checkItemsHCL declares one table whose only varying part is the CHECK
// expression, so two bundles differ exactly where a schema edit would.
func checkItemsHCL(expr string) string {
	return fmt.Sprintf(`
schema "public" {}
table %q {
  schema = schema.public
  column "id" {
    null = false
    type = text
  }
  column "state" {
    null = false
    type = varchar(20)
  }
  primary_key {
    columns = [column.id]
  }
  check %q {
    expr = %q
  }
}
`, checkTable, checkName, expr)
}

func applyCheckBundle(t *testing.T, dsn, expr string) {
	t.Helper()
	fs := fstest.MapFS{"migrations/schema.hcl": {Data: []byte(checkItemsHCL(expr))}}
	require.NoError(t, Apply(t.Context(), dsn, fs, WithDir("migrations"), WithName("check-changes")))
}

func checkDefinition(t *testing.T, db *sql.DB) (oid int64, definition string) {
	t.Helper()
	require.NoError(t, db.QueryRow(`SELECT c.oid::bigint, pg_get_constraintdef(c.oid)
FROM pg_constraint c JOIN pg_class t ON t.oid = c.conrelid
WHERE t.relname = $1 AND c.conname = $2`, checkTable, checkName).Scan(&oid, &definition))
	return oid, definition
}

// TestApplyUpdatesChangedCheckExpression proves an edited CHECK reaches a
// database that already has the constraint. Atlas's default diff matches checks
// by name and never compares their expressions, so a bundle that widened an
// allowed-values list left the old constraint in place and the first write of a
// new value failed at runtime with a check violation.
func TestApplyUpdatesChangedCheckExpression(t *testing.T) {
	handle := dbtest.ForT(t, dbtest.Options{Name: "check_expression_change"})
	dsn, db := handle.DSN(), handle.SQL()
	const (
		before = "((state)::text = ANY ((ARRAY['pending'::character varying, 'restored'::character varying])::text[]))"
		after  = "((state)::text = ANY ((ARRAY['pending'::character varying, 'restored'::character varying, 'observed'::character varying])::text[]))"
	)

	applyCheckBundle(t, dsn, before)
	_, definition := checkDefinition(t, db)
	require.NotContains(t, definition, "observed")

	applyCheckBundle(t, dsn, after)
	_, definition = checkDefinition(t, db)
	assert.Contains(t, definition, "'observed'")
	_, err := db.ExecContext(t.Context(), `INSERT INTO check_items (id, state) VALUES ('item-1', 'observed')`)
	assert.NoError(t, err, "the widened constraint should accept the new value")
}

// TestApplyIgnoresNormalizationOnlyCheckDifferences proves an expression written
// differently from PostgreSQL's canonical form is not rewritten on every apply.
// Comparing the raw strings would drop and re-add such a constraint each time a
// process opens the store, re-validating every row of the table under an
// exclusive lock.
func TestApplyIgnoresNormalizationOnlyCheckDifferences(t *testing.T) {
	handle := dbtest.ForT(t, dbtest.Options{Name: "check_expression_normalized"})
	dsn, db := handle.DSN(), handle.SQL()
	const expr = "(state)::text = ANY ((ARRAY['draft'::character varying, 'effective'::character varying])::text[])"

	applyCheckBundle(t, dsn, expr)
	firstOID, definition := checkDefinition(t, db)
	require.NotEqual(t, "CHECK ("+expr+")", definition,
		"fixture must differ from PostgreSQL's normalized form or it does not exercise normalization")

	applyCheckBundle(t, dsn, expr)
	secondOID, _ := checkDefinition(t, db)
	assert.Equal(t, firstOID, secondOID, "re-applying an equivalent expression must not recreate the constraint")
}
