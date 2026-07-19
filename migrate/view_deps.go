package migrate

import (
	"context"
	"database/sql"
	"fmt"

	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/commons-db/migrate/viewdeps"
	"github.com/flanksource/commons/logger"
	pgnodes "github.com/pgplex/pgparser/nodes"
	pgparser "github.com/pgplex/pgparser/parser"
)

// riskyModifiedTables returns the tables whose pending diff drops the table or drops/alters
// a column — the operations PostgreSQL refuses while a view depends on them. Pure additions
// never block a dependent view, so they are ignored.
func riskyModifiedTables(changes []schema.Change) []viewdeps.Table {
	var refs []viewdeps.Table
	seen := map[string]bool{}
	add := func(t *schema.Table) {
		if t == nil {
			return
		}
		// Atlas always reports a schema; default to public so a table it left
		// unqualified resolves the same way the diff did rather than through
		// whatever search_path this connection happens to carry.
		ref := viewdeps.Table{Schema: "public", Name: t.Name}
		if t.Schema != nil && t.Schema.Name != "" {
			ref.Schema = t.Schema.Name
		}
		if key := ref.Qualified(); !seen[key] {
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

// invalidateDependentViews clears every view that depends on a table the pending diff drops
// or alters, and returns a func that restores the ones this scope does not own.
//
// A view created by a post-phase script is dropped and its recorded hash deleted, so the post
// phase recreates it from source in the same apply. A view no script owns — an operator's
// ad-hoc reporting view, or one left by an older binary — is captured from the catalog first
// and rebuilt by the returned func, so a schema change neither fails on it nor destroys it.
//
// The returned set of invalidated script paths is empty when nothing needed changing. The
// restore func is never nil.
func invalidateDependentViews(ctx context.Context, db *sql.DB, scope string, changes []schema.Change, scripts map[string]*script) (map[string]bool, func(context.Context) error, error) {
	risky := riskyModifiedTables(changes)
	if len(risky) == 0 {
		return nil, noRestore, nil
	}
	owners := managedViews(scripts)
	log := logger.GetLogger("migrate")

	dropped, restore, err := viewdeps.Sweep(ctx, viewdeps.DropOptions{
		Tables: risky,
		Query:  db,
		Exec: func(ctx context.Context, stmt string) error {
			// DDL takes ACCESS EXCLUSIVE; bound the wait and retry so a live
			// reader cannot deadlock reconciliation indefinitely. Drops are
			// idempotent (IF EXISTS) and restores run once, so retry is safe.
			return retryOnLockContention(ctx, "reconcile dependent view", func() error {
				return execWithLockTimeout(ctx, db, stmt)
			})
		},
		Owned: func(v viewdeps.View) bool { _, ok := owners[v.Qualified()]; return ok },
		Logf:  func(format string, args ...any) { log.V(1).Infof(format, args...) },
	})
	if err != nil {
		return nil, nil, err
	}

	invalidated := map[string]bool{}
	for _, v := range dropped {
		if path, ok := owners[v.Qualified()]; ok {
			invalidated[path] = true
		}
	}
	for path := range invalidated {
		if _, err := db.ExecContext(ctx,
			`DELETE FROM schema_migration_scripts WHERE scope = $1 AND path = $2`, scope, path); err != nil {
			return nil, nil, fmt.Errorf("invalidate SQL migration %s: %w", path, err)
		}
	}
	return invalidated, restore, nil
}

func noRestore(context.Context) error { return nil }

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
