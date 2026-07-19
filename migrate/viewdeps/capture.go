package viewdeps

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Definition is everything needed to recreate a view exactly as it was. It is
// read from the catalog before the view is dropped, so a view no migration owns
// survives a schema change instead of blocking it or being lost.
type Definition struct {
	View
	// SQL is the view body from pg_get_viewdef. Note this is the *expanded*
	// definition: a view written as "SELECT a.*" comes back with every column
	// listed, so restoring after a column was dropped fails loudly rather than
	// silently changing the view's shape.
	SQL string
	// Indexes are complete CREATE INDEX statements. Materialized views only —
	// and load-bearing, since REFRESH ... CONCURRENTLY requires a unique index.
	Indexes []string
	Owner   string
	Comment string
	Grants  []string
}

const captureQuery = `
SELECT
  pg_get_viewdef(c.oid, true),
  pg_get_userbyid(c.relowner),
  COALESCE(obj_description(c.oid, 'pg_class'), '')
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = $2`

const captureIndexesQuery = `
SELECT indexdef FROM pg_indexes WHERE schemaname = $1 AND tablename = $2 ORDER BY indexname`

// captureGrantsQuery expands the ACL into one row per (grantee, privilege).
// A NULL relacl means default privileges only, and aclexplode yields no rows.
const captureGrantsQuery = `
SELECT
  CASE WHEN a.grantee = 0 THEN 'PUBLIC' ELSE pg_get_userbyid(a.grantee) END,
  a.privilege_type,
  a.is_grantable
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
CROSS JOIN LATERAL aclexplode(c.relacl) a
WHERE n.nspname = $1 AND c.relname = $2
ORDER BY 1, 2`

// Capture reads full definitions for views, preserving the given order.
func Capture(ctx context.Context, q Querier, views []View) ([]Definition, error) {
	if q == nil {
		return nil, fmt.Errorf("viewdeps: Querier is nil")
	}
	defs := make([]Definition, 0, len(views))
	for _, v := range views {
		def, err := captureOne(ctx, q, v)
		if err != nil {
			return nil, err
		}
		defs = append(defs, def)
	}
	return defs, nil
}

func captureOne(ctx context.Context, q Querier, v View) (Definition, error) {
	def := Definition{View: v}
	rows, err := q.QueryContext(ctx, captureQuery, v.Schema, v.Name)
	if err != nil {
		return def, fmt.Errorf("capture view %s: %w", v.Qualified(), err)
	}
	if err := scanOne(rows, &def.SQL, &def.Owner, &def.Comment); err != nil {
		return def, fmt.Errorf("capture view %s: %w", v.Qualified(), err)
	}
	if def.SQL == "" {
		return def, fmt.Errorf("capture view %s: not found", v.Qualified())
	}
	if v.Materialized() {
		if def.Indexes, err = captureStrings(ctx, q, captureIndexesQuery, v); err != nil {
			return def, fmt.Errorf("capture indexes for %s: %w", v.Qualified(), err)
		}
	}
	if def.Grants, err = captureGrants(ctx, q, v); err != nil {
		return def, fmt.Errorf("capture grants for %s: %w", v.Qualified(), err)
	}
	return def, nil
}

func scanOne(rows *sql.Rows, dest ...any) error {
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return err
		}
	}
	return rows.Err()
}

func captureStrings(ctx context.Context, q Querier, query string, v View) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, v.Schema, v.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func captureGrants(ctx context.Context, q Querier, v View) ([]string, error) {
	rows, err := q.QueryContext(ctx, captureGrantsQuery, v.Schema, v.Name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var grants []string
	for rows.Next() {
		var grantee, privilege string
		var grantable bool
		if err := rows.Scan(&grantee, &privilege, &grantable); err != nil {
			return nil, err
		}
		grants = append(grants, grantStatement(v, grantee, privilege, grantable))
	}
	return grants, rows.Err()
}

func grantStatement(v View, grantee, privilege string, grantable bool) string {
	// PUBLIC is a keyword, not an identifier, so it must not be quoted.
	target := quoteIdent(grantee)
	if grantee == "PUBLIC" {
		target = grantee
	}
	stmt := fmt.Sprintf("GRANT %s ON %s.%s TO %s",
		privilege, quoteIdent(v.Schema), quoteIdent(v.Name), target)
	if grantable {
		stmt += " WITH GRANT OPTION"
	}
	return stmt
}

// CreateStatements returns the ordered DDL that recreates the view: the view
// itself, then owner, indexes, comment and grants. Materialized views are
// created WITH DATA so they are queryable immediately — an unpopulated
// materialized view errors on every read until someone refreshes it.
func (d Definition) CreateStatements() []string {
	body := strings.TrimSuffix(strings.TrimSpace(d.SQL), ";")
	kind, name := "VIEW", quoteIdent(d.Schema)+"."+quoteIdent(d.Name)
	create := fmt.Sprintf("CREATE VIEW %s AS %s", name, body)
	if d.Materialized() {
		kind = "MATERIALIZED VIEW"
		create = fmt.Sprintf("CREATE MATERIALIZED VIEW %s AS %s WITH DATA", name, body)
	}

	stmts := []string{create}
	if d.Owner != "" {
		stmts = append(stmts, fmt.Sprintf("ALTER %s %s OWNER TO %s", kind, name, quoteIdent(d.Owner)))
	}
	stmts = append(stmts, d.Indexes...)
	if d.Comment != "" {
		stmts = append(stmts, fmt.Sprintf("COMMENT ON %s %s IS %s", kind, name, quoteLiteral(d.Comment)))
	}
	return append(stmts, d.Grants...)
}

func quoteLiteral(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}

// RestoreError reports a view that was dropped to allow a schema change and
// could not be recreated. It carries the full DDL so an operator can replay it
// by hand — the database is otherwise migrated correctly but missing the view.
type RestoreError struct {
	View       View
	Statements []string
	Err        error
}

func (e *RestoreError) Error() string {
	return fmt.Sprintf("restore view %s: %v\n"+
		"the view was dropped to allow the schema change and could not be recreated.\n"+
		"Recreate it manually with:\n\n  %s",
		e.View.Qualified(), e.Err, strings.Join(e.Statements, ";\n  ")+";")
}

func (e *RestoreError) Unwrap() error { return e.Err }

// Restore recreates definitions in the order given, which Dependents guarantees
// is dependency order. The first failure aborts and returns a *RestoreError
// naming the view and carrying its DDL.
func Restore(ctx context.Context, exec Exec, defs []Definition) error {
	if exec == nil {
		return fmt.Errorf("viewdeps: Exec is nil")
	}
	for _, def := range defs {
		stmts := def.CreateStatements()
		for _, stmt := range stmts {
			if err := exec(ctx, stmt); err != nil {
				return &RestoreError{View: def.View, Statements: stmts, Err: err}
			}
		}
	}
	return nil
}
