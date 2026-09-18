package sqlitetable

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	unsafeRun      = regexp.MustCompile(`[^A-Za-z0-9_]+`)
	safeIdentifier = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

// PhysicalNames derives the column each declared name is stored in: every run
// of characters outside [A-Za-z0-9_] becomes "_" and the ends are trimmed; an
// empty result or a leading digit is prefixed "c_"; a name bare rejects gets a
// trailing "_"; and a name equal, ignoring case, to a reserved name or an
// earlier column is numbered "_2", "_3", …. Declared order is kept.
func PhysicalNames(declared, reserved []string, bare func(name string) (bool, error)) ([]string, error) {
	taken := make(map[string]bool, len(declared)+len(reserved))
	for _, name := range reserved {
		taken[strings.ToLower(name)] = true
	}
	physical := make([]string, len(declared))
	for index, name := range declared {
		safe := strings.Trim(unsafeRun.ReplaceAllString(name, "_"), "_")
		if safe == "" || safe[0] >= '0' && safe[0] <= '9' {
			safe = "c_" + safe
		}
		standsBare, err := bare(safe)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", name, err)
		}
		if !standsBare {
			safe += "_"
		}
		unique := safe
		for suffix := 2; taken[strings.ToLower(unique)]; suffix++ {
			unique = fmt.Sprintf("%s_%d", safe, suffix)
		}
		taken[strings.ToLower(unique)] = true
		physical[index] = unique
	}
	return physical, nil
}

// Queryer is what reading a table's shape needs from a connection: a *sql.DB
// or a *sql.Tx.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// PhysicalColumns are the columns the table named table has, in order.
func PhysicalColumns(ctx context.Context, database Queryer, table string) ([]string, error) {
	rows, err := database.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, fmt.Errorf("read columns of %q: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("read columns of %q: %w", table, err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read columns of %q: %w", table, err)
	}
	return names, nil
}

// RenamePositional renames the columns of a table an older build stored
// positionally, "c0"…"c<n>", to StoredAs. Renames carry the table's keys and
// indexes along. Every column first moves aside to a name no derivation
// produces, since a derived name may be another column's positional one.
func (t Table) RenamePositional(ctx context.Context, database interface {
	Execer
	Queryer
}) error {
	if err := t.checkStoredAs(); err != nil {
		return err
	}
	existing, err := PhysicalColumns(ctx, database, t.Name)
	if err != nil {
		return err
	}
	positional := make([]string, len(t.Columns))
	aside := make([]string, len(t.Columns))
	for index := range t.Columns {
		positional[index], aside[index] = fmt.Sprintf("c%d", index), fmt.Sprintf("_c%d", index)
	}
	if !slices.Equal(existing, positional) {
		return fmt.Errorf("table %q is not positional: it has columns %q, not %q", t.Name, existing, positional)
	}
	for _, rename := range [][2][]string{{positional, aside}, {aside, t.StoredAs}} {
		for index := range t.Columns {
			statement := fmt.Sprintf(`ALTER TABLE %s RENAME COLUMN %s TO %s`,
				QuoteIdentifier(t.Name), QuoteIdentifier(rename[0][index]), QuoteIdentifier(rename[1][index]))
			if _, err := database.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("table %q: rename column %q to %q: %w", t.Name, rename[0][index], rename[1][index], err)
			}
		}
	}
	return nil
}

// Bare reports, by preparing `SELECT 1 AS <name>` on database, whether SQLite's
// own parser accepts name as an unquoted identifier. Only a parse failure is a
// "no"; any other failure is returned.
func Bare(ctx context.Context, database Execer) func(name string) (bool, error) {
	return func(name string) (bool, error) {
		if !safeIdentifier.MatchString(name) {
			return false, fmt.Errorf("%q is not a safe identifier", name)
		}
		statement, err := database.PrepareContext(ctx, "SELECT 1 AS "+name)
		if err == nil {
			return true, statement.Close()
		}
		var sqliteErr *sqlite.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_ERROR {
			return false, nil
		}
		return false, fmt.Errorf("check identifier %q: %w", name, err)
	}
}
