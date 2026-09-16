package migrate

import (
	"context"
	"database/sql"
	"fmt"

	"ariga.io/atlas/sql/schema"
	"github.com/lib/pq"
)

// withCheckExpressionChanges adds a ModifyCheck for every CHECK the bundle edited
// in place. Atlas's non-normalized diff pairs checks by name and never compares
// their expressions, so without this an edited constraint never reaches a
// database that already holds the old one. Both sides are compared as
// PostgreSQL renders them, so HCL written in any equivalent form does not drop
// and re-add the constraint — re-validating every row — on each apply.
func withCheckExpressionChanges(ctx context.Context, db *sql.DB, schemaName string, current, desired *schema.Realm, changes []schema.Change) ([]schema.Change, error) {
	currentSchema, ok := current.Schema(schemaName)
	if !ok {
		return changes, nil
	}
	desiredSchema, ok := desired.Schema(schemaName)
	if !ok {
		return changes, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin check expression normalization: %w", err)
	}
	// Normalization only creates scratch temp tables; nothing it does is kept.
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '"+migrationLockTimeout+"'"); err != nil {
		return nil, fmt.Errorf("bound check expression normalization lock wait: %w", err)
	}
	for i, to := range desiredSchema.Tables {
		from, ok := currentSchema.Table(to.Name)
		if !ok {
			continue
		}
		modified, err := modifiedChecks(ctx, tx, modifiedChecksOptions{
			Schema: schemaName, From: from, To: to, TempTable: fmt.Sprintf("commons_db_check_%d", i),
		})
		if err != nil {
			return nil, err
		}
		if len(modified) > 0 {
			changes = appendCheckChanges(changes, to, modified)
		}
	}
	return changes, nil
}

type modifiedChecksOptions struct {
	Schema    string
	From, To  *schema.Table
	TempTable string
}

// modifiedChecks returns the same-named checks whose normalized expressions
// differ. The desired expression is normalized by adding it to an empty copy of
// the table, which resolves columns, casts and functions exactly as the real
// constraint was resolved.
func modifiedChecks(ctx context.Context, tx *sql.Tx, opts modifiedChecksOptions) ([]*schema.ModifyCheck, error) {
	var candidates []*schema.ModifyCheck
	for _, desired := range tableChecks(opts.To) {
		for _, existing := range tableChecks(opts.From) {
			if existing.Name == desired.Name && existing.Expr != desired.Expr {
				candidates = append(candidates, &schema.ModifyCheck{From: existing, To: desired})
			}
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	table := pq.QuoteIdentifier(opts.Schema) + "." + pq.QuoteIdentifier(opts.To.Name)
	temp := "pg_temp." + pq.QuoteIdentifier(opts.TempTable)
	if _, err := tx.ExecContext(ctx, "CREATE TEMP TABLE "+pq.QuoteIdentifier(opts.TempTable)+" (LIKE "+table+")"); err != nil {
		return nil, fmt.Errorf("copy %s to normalize its checks: %w", table, err)
	}
	var modified []*schema.ModifyCheck
	for _, candidate := range candidates {
		name := candidate.To.Name
		if _, err := tx.ExecContext(ctx, "ALTER TABLE "+temp+" ADD CONSTRAINT "+pq.QuoteIdentifier(name)+" CHECK ("+candidate.To.Expr+")"); err != nil {
			return nil, fmt.Errorf("normalize check %s on %s: %w", name, table, err)
		}
		var normalized, live string
		if err := tx.QueryRowContext(ctx,
			`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = $1::regclass AND conname = $2`,
			temp, name).Scan(&normalized); err != nil {
			return nil, fmt.Errorf("read normalized check %s on %s: %w", name, table, err)
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = $1::regclass AND conname = $2`,
			table, name).Scan(&live); err != nil {
			return nil, fmt.Errorf("read live check %s on %s: %w", name, table, err)
		}
		if normalized != live {
			modified = append(modified, candidate)
		}
	}
	return modified, nil
}

// appendCheckChanges folds the modified checks into the table's pending
// ModifyTable, skipping any check Atlas already changes, or adds one.
func appendCheckChanges(changes []schema.Change, table *schema.Table, modified []*schema.ModifyCheck) []schema.Change {
	for _, change := range changes {
		alter, ok := change.(*schema.ModifyTable)
		if !ok || alter.T.Name != table.Name {
			continue
		}
		for _, check := range modified {
			if !changesCheck(alter.Changes, check.To.Name) {
				alter.Changes = append(alter.Changes, check)
			}
		}
		return changes
	}
	alter := &schema.ModifyTable{T: table}
	for _, check := range modified {
		alter.Changes = append(alter.Changes, check)
	}
	return append(changes, alter)
}

func changesCheck(changes []schema.Change, name string) bool {
	for _, change := range changes {
		switch c := change.(type) {
		case *schema.ModifyCheck:
			if c.To.Name == name {
				return true
			}
		case *schema.AddCheck:
			if c.C.Name == name {
				return true
			}
		case *schema.DropCheck:
			if c.C.Name == name {
				return true
			}
		}
	}
	return false
}

func tableChecks(table *schema.Table) []*schema.Check {
	var checks []*schema.Check
	for _, attr := range table.Attrs {
		if check, ok := attr.(*schema.Check); ok {
			checks = append(checks, check)
		}
	}
	return checks
}
