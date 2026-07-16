package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/commons/logger"
	"github.com/lib/pq"
	pgnodes "github.com/pgplex/pgparser/nodes"
	pgparser "github.com/pgplex/pgparser/parser"
)

// tableRef identifies a table Atlas is about to reshape.
type tableRef struct {
	schemaName string
	name       string
}

func (t tableRef) qualified() string {
	s := t.schemaName
	if s == "" {
		s = "public"
	}
	return s + "." + t.name
}

// viewRef identifies a live view (relkind 'v') or materialized view ('m').
type viewRef struct {
	schemaName string
	name       string
	relkind    string
}

func (v viewRef) qualified() string { return v.schemaName + "." + v.name }

// riskyModifiedTables returns the tables whose pending diff drops the table or drops/alters
// a column — the operations PostgreSQL refuses while a view depends on them. Pure additions
// never block a dependent view, so they are ignored.
func riskyModifiedTables(changes []schema.Change) []tableRef {
	var refs []tableRef
	seen := map[string]bool{}
	add := func(t *schema.Table) {
		if t == nil {
			return
		}
		ref := tableRef{name: t.Name}
		if t.Schema != nil {
			ref.schemaName = t.Schema.Name
		}
		if key := ref.qualified(); !seen[key] {
			seen[key] = true
			refs = append(refs, ref)
		}
	}
	for _, c := range changes {
		switch ch := c.(type) {
		case *schema.DropTable:
			add(ch.T)
		case *schema.ModifyTable:
			for _, cc := range ch.Changes {
				switch cc.(type) {
				case *schema.DropColumn, *schema.ModifyColumn:
					add(ch.T)
				}
			}
		}
	}
	return refs
}

// managedViews maps each fully-qualified view name to the post-phase SQL script that creates
// it, using pgparser to read CREATE [MATERIALIZED] VIEW targets. The SQL grammar treats
// PL/pgSQL bodies as opaque literals, so mixed view/function/trigger files parse cleanly;
// a script that fails to parse is logged and skipped (its views fall through as unmanaged).
func managedViews(scripts map[string]*script) map[string]string {
	owners := map[string]string{}
	for _, s := range scripts {
		if s.phase != phasePost {
			continue
		}
		stmts, err := pgparser.Parse(s.content)
		if err != nil {
			logger.GetLogger("migrate").Warnf("skip view attribution for %s: %v", s.path, err)
			continue
		}
		for _, name := range viewNames(stmts) {
			owners[name] = s.path
		}
	}
	return owners
}

func viewNames(list *pgnodes.List) []string {
	if list == nil {
		return nil
	}
	var names []string
	for _, item := range list.Items {
		switch stmt := item.(type) {
		case *pgnodes.ViewStmt:
			names = append(names, qualifyRangeVar(stmt.View))
		case *pgnodes.CreateTableAsStmt:
			if stmt.Objtype == pgnodes.OBJECT_MATVIEW && stmt.Into != nil {
				names = append(names, qualifyRangeVar(stmt.Into.Rel))
			}
		}
	}
	return names
}

func qualifyRangeVar(rv *pgnodes.RangeVar) string {
	if rv == nil {
		return ""
	}
	schemaName := rv.Schemaname
	if schemaName == "" {
		schemaName = "public"
	}
	return schemaName + "." + rv.Relname
}

// dependentViewsQuery returns every view/matview that transitively depends on any of the
// given tables, walking pg_rewrite rules through pg_depend.
const dependentViewsQuery = `
WITH RECURSIVE targets(oid) AS (
  SELECT to_regclass(t)::oid
  FROM unnest($1::text[]) AS t
  WHERE to_regclass(t) IS NOT NULL
),
deps(view_oid) AS (
  SELECT r.ev_class
  FROM pg_depend d
  JOIN pg_rewrite r ON r.oid = d.objid AND d.classid = 'pg_rewrite'::regclass
  JOIN targets t ON t.oid = d.refobjid
  WHERE r.ev_class <> d.refobjid
  UNION
  SELECT r.ev_class
  FROM deps
  JOIN pg_depend d ON d.refobjid = deps.view_oid AND d.refclassid = 'pg_class'::regclass
  JOIN pg_rewrite r ON r.oid = d.objid AND d.classid = 'pg_rewrite'::regclass
  WHERE r.ev_class <> d.refobjid
)
SELECT DISTINCT n.nspname, c.relname, c.relkind::text
FROM deps
JOIN pg_class c ON c.oid = deps.view_oid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('v', 'm')`

func dependentViews(ctx context.Context, db *sql.DB, tables []tableRef) ([]viewRef, error) {
	names := make([]string, len(tables))
	for i, t := range tables {
		names[i] = t.qualified()
	}
	rows, err := db.QueryContext(ctx, dependentViewsQuery, pq.Array(names))
	if err != nil {
		return nil, fmt.Errorf("inspect view dependencies: %w", err)
	}
	defer rows.Close()
	var refs []viewRef
	for rows.Next() {
		var v viewRef
		if err := rows.Scan(&v.schemaName, &v.name, &v.relkind); err != nil {
			return nil, fmt.Errorf("read view dependency: %w", err)
		}
		refs = append(refs, v)
	}
	return refs, rows.Err()
}

// invalidateDependentViews drops every commons-db-managed view that depends on a table the
// pending diff drops or alters, and deletes the recorded hash of the script that creates it
// so the post phase recreates the view in the same apply. If any dependent view is owned by
// no migration it aborts before dropping anything — leaving the database untouched — and
// returns a loud error naming the view, since reconciliation would otherwise fail with the
// managed views already gone. The returned set of invalidated script paths is empty when
// nothing needed changing.
func invalidateDependentViews(ctx context.Context, db *sql.DB, scope string, changes []schema.Change, scripts map[string]*script) (map[string]bool, error) {
	risky := riskyModifiedTables(changes)
	if len(risky) == 0 {
		return nil, nil
	}
	dependents, err := dependentViews(ctx, db, risky)
	if err != nil {
		return nil, err
	}
	if len(dependents) == 0 {
		return nil, nil
	}
	owners := managedViews(scripts)
	var managed []viewRef
	var unmanaged []string
	for _, v := range dependents {
		if _, ok := owners[v.qualified()]; ok {
			managed = append(managed, v)
		} else {
			unmanaged = append(unmanaged, v.qualified())
		}
	}
	if len(unmanaged) > 0 {
		sort.Strings(unmanaged)
		return nil, fmt.Errorf("cannot reconcile tables: dependent view(s) %s are not managed by any %q migration; add a post-phase script that (re)creates them or drop them manually",
			strings.Join(unmanaged, ", "), scope)
	}
	invalidated := map[string]bool{}
	for _, v := range managed {
		if err := dropDependentView(ctx, db, v); err != nil {
			return nil, err
		}
		invalidated[owners[v.qualified()]] = true
	}
	for path := range invalidated {
		if _, err := db.ExecContext(ctx,
			`DELETE FROM schema_migration_scripts WHERE scope = $1 AND path = $2`, scope, path); err != nil {
			return nil, fmt.Errorf("invalidate SQL migration %s: %w", path, err)
		}
	}
	return invalidated, nil
}

func dropDependentView(ctx context.Context, db *sql.DB, v viewRef) error {
	kind := "VIEW"
	if v.relkind == "m" {
		kind = "MATERIALIZED VIEW"
	}
	stmt := fmt.Sprintf("DROP %s IF EXISTS %s.%s CASCADE", kind,
		pq.QuoteIdentifier(v.schemaName), pq.QuoteIdentifier(v.name))
	// DROP takes ACCESS EXCLUSIVE on the view; bound the wait and retry so a
	// live reader cannot deadlock the reconciliation indefinitely. The DROP is
	// idempotent (IF EXISTS), so re-running the transaction is safe.
	if err := retryOnLockContention(ctx, "drop dependent view "+v.qualified(), func() error {
		return execWithLockTimeout(ctx, db, stmt)
	}); err != nil {
		return fmt.Errorf("drop dependent view %s: %w", v.qualified(), err)
	}
	logger.GetLogger("migrate").V(1).Infof("dropped dependent view %s for table reconciliation", v.qualified())
	return nil
}

// execWithLockTimeout runs a single statement in a transaction that first bounds
// its lock waits, so DDL cannot camp on an ACCESS EXCLUSIVE lock against live
// traffic.
func execWithLockTimeout(ctx context.Context, db *sql.DB, stmt string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '"+migrationLockTimeout+"'"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, stmt); err != nil {
		return err
	}
	return tx.Commit()
}
