package metrics

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ProfileTable is the result of a profile referenced as `profiles.<name>` in
// metric SQL: its column names and its rows.
type ProfileTable struct {
	Columns []string
	Rows    []map[string]any
}

// ProfileRows executes the named profile for a `profiles.<name>` reference.
type ProfileRows func(name string) (ProfileTable, error)

func loadProfile(ctx context.Context, db *sql.DB, name, table string, profiles ProfileRows) error {
	result, err := profiles(name)
	if err != nil {
		return fmt.Errorf("profiles.%s: %w", name, err)
	}
	if len(result.Columns) == 0 {
		return fmt.Errorf("profiles.%s has no columns: declare columns or return at least one row", name)
	}
	defs := make([]string, len(result.Columns))
	for i, c := range result.Columns {
		defs[i] = quote(c)
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+quote(table)+" ("+strings.Join(defs, ",")+")"); err != nil {
		return fmt.Errorf("profiles.%s: %w", name, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // a no-op once committed
	stmt, err := tx.PrepareContext(ctx, "INSERT INTO "+quote(table)+" VALUES ("+strings.TrimSuffix(strings.Repeat("?,", len(defs)), ",")+")")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, row := range result.Rows {
		values := make([]any, len(result.Columns))
		for i, c := range result.Columns {
			if values[i], err = profileCell(row[c]); err != nil {
				return fmt.Errorf("profiles.%s column %q: %w", name, c, err)
			}
		}
		if _, err := stmt.ExecContext(ctx, values...); err != nil {
			return fmt.Errorf("profiles.%s: %w", name, err)
		}
	}
	return tx.Commit()
}

// profileCell stores a profile value the way metric tables do: timestamps as
// sortable RFC3339 text, structured values as JSON SQLite's json functions read.
func profileCell(v any) (any, error) {
	switch v := v.(type) {
	case nil, string, bool, int, int32, int64, float32, float64, []byte:
		return v, nil
	case time.Time:
		return formatTime(v), nil
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return string(encoded), nil
	}
}
