// Package viewdeps finds, drops, and restores the views and materialized views
// that transitively depend on a set of tables, so DDL PostgreSQL refuses while a
// dependent view exists — DROP TABLE, DROP COLUMN, ALTER COLUMN TYPE — can
// proceed.
//
// Discovery walks pg_depend/pg_rewrite rather than matching view names, so it is
// complete regardless of how a view is named or which schema it lives in. A
// name-convention sweep silently misses a view outside the connection's
// search_path — the DROP resolves to nothing and succeeds — and the blocked DDL
// then fails much later with a confusing error.
//
// The package deliberately depends only on the standard library so callers can
// use it with any driver: *sql.DB, *sql.Tx and gorm's connection pool all
// satisfy Querier as-is.
package viewdeps

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Table identifies a relation whose dependents must be cleared. An empty Schema
// is resolved through the connection's search_path, which is usually what a
// caller migrating its own database wants; set it only to target a specific
// schema.
type Table struct {
	Schema string
	Name   string
}

// Qualified renders the table for to_regclass. An empty Schema is left
// unqualified deliberately: forcing "public" would target the wrong relation
// whenever the connection runs under a different search_path.
func (t Table) Qualified() string {
	if t.Schema == "" {
		return t.Name
	}
	return t.Schema + "." + t.Name
}

// Tables builds refs for several relations in one schema. An empty schema
// resolves through search_path.
func Tables(schema string, names ...string) []Table {
	tables := make([]Table, 0, len(names))
	for _, name := range names {
		tables = append(tables, Table{Schema: schema, Name: name})
	}
	return tables
}

// View identifies a live view or materialized view. Kind is pg_class.relkind:
// "v" for a view, "m" for a materialized view.
type View struct {
	Schema string
	Name   string
	Kind   string
}

// Qualified renders the view schema-qualified. Unlike Table, a View always
// carries the schema the catalog reported, so this is never ambiguous.
func (v View) Qualified() string { return v.Schema + "." + v.Name }

// Materialized reports whether the view holds its own copy of the data.
func (v View) Materialized() bool { return v.Kind == relkindMatview }

// DropStatement returns the schema-qualified, identifier-quoted DROP. CASCADE is
// unconditional: a view built on this one is itself a dependent that has already
// been captured, so taking it here is both safe and necessary.
func (v View) DropStatement() string {
	return fmt.Sprintf("DROP %s IF EXISTS %s.%s CASCADE",
		v.dropKeyword(), quoteIdent(v.Schema), quoteIdent(v.Name))
}

func (v View) dropKeyword() string {
	if v.Materialized() {
		return "MATERIALIZED VIEW"
	}
	return "VIEW"
}

const relkindMatview = "m"

// Querier reads the dependency graph. *sql.DB, *sql.Tx, *sql.Conn and gorm's
// ConnPool all satisfy it without an adapter.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Exec runs a single DDL statement. Callers supply the execution policy: the
// migrate package wraps each statement in a bounded lock_timeout with retry, and
// a gorm caller routes through *gorm.DB so an open transaction stays in scope.
type Exec func(ctx context.Context, stmt string) error

// dependentsQuery returns every view and materialized view that transitively
// depends on any of the given relations, walking pg_rewrite rules through
// pg_depend. Relations that do not exist are skipped by the to_regclass filter
// rather than erroring, so a caller can pass its full table list before those
// tables have been created.
//
// Rows come back in dependency order. Each row carries the longest path from a
// target table (UNION ALL rather than UNION so depth accumulates; view
// dependencies are acyclic, so this terminates), and taking MAX per view gives
// an order in which every view appears after everything it reads. Restoring in
// that order means a view built on another view is always created second.
const dependentsQuery = `
WITH RECURSIVE targets(oid) AS (
  SELECT to_regclass(t)::oid
  FROM unnest(ARRAY[%s]::text[]) AS t
  WHERE to_regclass(t) IS NOT NULL
),
deps(view_oid, depth) AS (
  SELECT r.ev_class, 1
  FROM pg_depend d
  JOIN pg_rewrite r ON r.oid = d.objid AND d.classid = 'pg_rewrite'::regclass
  JOIN targets t ON t.oid = d.refobjid
  WHERE r.ev_class <> d.refobjid
  UNION ALL
  SELECT r.ev_class, deps.depth + 1
  FROM deps
  JOIN pg_depend d ON d.refobjid = deps.view_oid AND d.refclassid = 'pg_class'::regclass
  JOIN pg_rewrite r ON r.oid = d.objid AND d.classid = 'pg_rewrite'::regclass
  WHERE r.ev_class <> d.refobjid
)
SELECT n.nspname, c.relname, c.relkind::text
FROM deps
JOIN pg_class c ON c.oid = deps.view_oid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('v', 'm')
GROUP BY 1, 2, 3
ORDER BY MAX(deps.depth), 1, 2`

// Dependents returns every view and materialized view that transitively depends
// on any of tables, in dependency order: a view always follows everything it
// reads. Tables that do not exist are ignored; the tables themselves are never
// returned.
func Dependents(ctx context.Context, q Querier, tables []Table) ([]View, error) {
	if q == nil {
		return nil, fmt.Errorf("viewdeps: Querier is nil")
	}
	if len(tables) == 0 {
		return nil, nil
	}
	args := make([]any, len(tables))
	for i, t := range tables {
		args[i] = t.Qualified()
	}
	rows, err := q.QueryContext(ctx, fmt.Sprintf(dependentsQuery, arrayPlaceholders(len(args))), args...)
	if err != nil {
		return nil, fmt.Errorf("inspect view dependencies: %w", err)
	}
	return scanViews(rows)
}

// lookupQuery resolves relation names to their real schema. Bare names go
// through search_path, so this is the safe way to turn a name a caller
// constructed into something that can be dropped without relying on the
// session's search_path at DROP time.
const lookupQuery = `
SELECT DISTINCT n.nspname, c.relname, c.relkind::text
FROM unnest(ARRAY[%s]::text[]) AS t
JOIN pg_class c ON c.oid = to_regclass(t)
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('v', 'm')
ORDER BY 1, 2`

// Lookup resolves relation names to schema-qualified view refs, skipping names
// that do not exist or name something that is not a view.
func Lookup(ctx context.Context, q Querier, names ...string) ([]View, error) {
	if q == nil {
		return nil, fmt.Errorf("viewdeps: Querier is nil")
	}
	if len(names) == 0 {
		return nil, nil
	}
	args := make([]any, len(names))
	for i, name := range names {
		args[i] = name
	}
	rows, err := q.QueryContext(ctx, fmt.Sprintf(lookupQuery, arrayPlaceholders(len(args))), args...)
	if err != nil {
		return nil, fmt.Errorf("resolve views: %w", err)
	}
	return scanViews(rows)
}

func scanViews(rows *sql.Rows) ([]View, error) {
	defer rows.Close()
	var views []View
	for rows.Next() {
		var v View
		if err := rows.Scan(&v.Schema, &v.Name, &v.Kind); err != nil {
			return nil, fmt.Errorf("read view row: %w", err)
		}
		views = append(views, v)
	}
	return views, rows.Err()
}

// arrayPlaceholders renders "$1,$2,...,$n" for an n-element array literal.
// Building the array in SQL rather than binding a driver array type keeps the
// query portable across lib/pq, pgx and gorm.
func arrayPlaceholders(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("$%d", i+1)
	}
	return strings.Join(parts, ",")
}

// quoteIdent renders a PostgreSQL identifier safely, doubling any embedded
// quote. Always quoting means mixed-case and reserved names survive a
// drop/recreate round-trip unchanged.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
