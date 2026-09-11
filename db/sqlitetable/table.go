// Package sqlitetable writes rows described by query.ColumnDef into SQLite
// tables that a `sql` profile can read back under the declared column names.
//
// Columns are stored positionally ("c0", "c1", …) and aliased on the way out,
// so a declared name is never an identifier the table itself has to carry —
// any string a profile can name a column is a name this package can store.
package sqlitetable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	// Pure-Go sqlite driver. Must be modernc, never github.com/glebarez/go-sqlite
	// — both register the "sqlite" driver name and linking both panics at init.
	// See connection/sql.go for the full rationale.
	_ "modernc.org/sqlite"

	"github.com/flanksource/commons-db/query"
)

// Execer is what writing a table needs from a connection: a *sql.DB or a
// *sql.Tx.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

// Table is one SQLite table and the declared columns it stores.
type Table struct {
	Name    string
	Columns []query.ColumnDef

	// PrimaryKey names the declared columns that together identify a row.
	PrimaryKey []string

	// Unique names declared columns that each get their own unique index.
	Unique []string
}

// Write creates the table and inserts rows into it.
func Write(ctx context.Context, database Execer, table Table, rows []query.Row) error {
	if err := table.Create(ctx, database); err != nil {
		return err
	}
	return table.Insert(ctx, database, rows)
}

// Create creates the table and its unique indexes. The table must not exist.
func (t Table) Create(ctx context.Context, database Execer) error {
	definitions := make([]string, len(t.Columns), len(t.Columns)+1)
	for index, column := range t.Columns {
		definitions[index] = fmt.Sprintf(`"c%d" %s`, index, Type(column.Type))
	}
	if len(t.PrimaryKey) > 0 {
		keys, err := t.physicalList(t.PrimaryKey)
		if err != nil {
			return err
		}
		definitions = append(definitions, "PRIMARY KEY ("+keys+")")
	}
	statement := fmt.Sprintf(`CREATE TABLE %s (%s)`, QuoteIdentifier(t.Name), strings.Join(definitions, ", "))
	if _, err := database.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("create table %q: %w", t.Name, err)
	}
	for _, name := range t.Unique {
		physical, err := t.Physical(name)
		if err != nil {
			return err
		}
		statement := fmt.Sprintf(`CREATE UNIQUE INDEX %s ON %s (%s)`,
			QuoteIdentifier(t.Name+"_"+name), QuoteIdentifier(t.Name), physical)
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("index table %q: %w", t.Name, err)
		}
	}
	return nil
}

// Insert appends rows to the existing table. A row key the table does not
// declare is not stored; a declared column the row lacks is stored as NULL.
func (t Table) Insert(ctx context.Context, database Execer, rows []query.Row) error {
	if len(rows) == 0 {
		return nil
	}
	markers := strings.TrimRight(strings.Repeat("?,", len(t.Columns)), ",")
	statement, err := database.PrepareContext(ctx, fmt.Sprintf(`INSERT INTO %s VALUES (%s)`, QuoteIdentifier(t.Name), markers))
	if err != nil {
		return fmt.Errorf("prepare table %q: %w", t.Name, err)
	}
	defer func() { _ = statement.Close() }()
	for rowIndex, row := range rows {
		values := make([]any, len(t.Columns))
		for columnIndex, column := range t.Columns {
			values[columnIndex], err = Value(column.Type, row[column.Name])
			if err != nil {
				return fmt.Errorf("row %d column %q: %w", rowIndex, column.Name, err)
			}
		}
		if _, err := statement.ExecContext(ctx, values...); err != nil {
			return fmt.Errorf("insert row %d into %q: %w", rowIndex, t.Name, err)
		}
	}
	return nil
}

// Select reads every declared column under its declared name.
func (t Table) Select() string {
	selects := make([]string, len(t.Columns))
	for index, column := range t.Columns {
		selects[index] = fmt.Sprintf(`"c%d" AS %s`, index, QuoteIdentifier(column.Name))
	}
	return fmt.Sprintf(`SELECT %s FROM %s`, strings.Join(selects, ", "), QuoteIdentifier(t.Name))
}

// Physical is the quoted column a declared column is stored in, for a clause
// that has to address the table itself rather than the aliased result.
func (t Table) Physical(name string) (string, error) {
	index := slices.IndexFunc(t.Columns, func(column query.ColumnDef) bool { return column.Name == name })
	if index < 0 {
		return "", fmt.Errorf("table %q has no column %q", t.Name, name)
	}
	return fmt.Sprintf(`"c%d"`, index), nil
}

func (t Table) physicalList(names []string) (string, error) {
	physical := make([]string, len(names))
	for index, name := range names {
		column, err := t.Physical(name)
		if err != nil {
			return "", err
		}
		physical[index] = column
	}
	return strings.Join(physical, ", "), nil
}

// TimeLayout is how an instant is stored, and how a bound compared against a
// stored instant must be bound. SQLite has no time type, so instants are TEXT
// compared as text, and text only orders as time when every value is written
// to the same width in the same zone: RFC3339Nano drops trailing zeros, so
// "…05Z" would sort after "…05.5Z", and the driver's rendering of a bound
// time.Time is Go's String(), which sorts before every stored value of its day.
const TimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// FormatTime is at in TimeLayout, in UTC.
func FormatTime(at time.Time) string { return at.UTC().Format(TimeLayout) }

// Value converts a row value into one the sqlite driver stores: declared
// structured values as JSON text, other scalars as themselves, and times in
// TimeLayout.
func Value(columnType query.ColumnType, value any) (any, error) {
	if value != nil && IsStructured(columnType) {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return string(encoded), nil
	}
	switch typed := value.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, string, []byte:
		return typed, nil
	case time.Time:
		return FormatTime(typed), nil
	case *time.Time:
		if typed == nil {
			return nil, nil
		}
		return FormatTime(*typed), nil
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return string(encoded), nil
	}
}

// Type is the SQLite column type a declared column type is stored as. A
// boolean is declared BOOLEAN rather than INTEGER: SQLite stores both as 0/1,
// but the driver reads a BOOLEAN column back as a Go bool, so a profile over
// the table returns true/false rather than a number.
func Type(columnType query.ColumnType) string {
	switch columnType {
	case query.ColumnTypeNumber, query.ColumnTypeDuration, query.ColumnTypeBytes:
		return "NUMERIC"
	case query.ColumnTypeBoolean:
		return "BOOLEAN"
	default:
		return "TEXT"
	}
}

// IsStructured reports whether a column type is stored as JSON text.
func IsStructured(columnType query.ColumnType) bool {
	return columnType == query.ColumnTypeJSON || columnType == query.ColumnTypeKeyValue || columnType == query.ColumnTypeKeyValues
}

// DecodeStructured parses the JSON text of every structured column in row in
// place, so a row read back holds the value that was written.
func DecodeStructured(columns []query.ColumnDef, row query.Row) error {
	for _, column := range columns {
		if !IsStructured(column.Type) {
			continue
		}
		encoded, ok := row[column.Name].(string)
		if !ok || encoded == "" {
			continue
		}
		var decoded any
		decoder := json.NewDecoder(strings.NewReader(encoded))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			return fmt.Errorf("decode structured column %q: %w", column.Name, err)
		}
		row[column.Name] = decoded
	}
	return nil
}

// ProfileColumns are the columns a profile over the table declares: each
// structured column reads its stored JSON text back as the value it encodes.
func ProfileColumns(columns []query.ColumnDef) []query.ColumnDef {
	profileColumns := slices.Clone(columns)
	for index := range profileColumns {
		if IsStructured(profileColumns[index].Type) {
			profileColumns[index].Source = profileColumns[index].Name
			profileColumns[index].JSONPath = "$"
		}
	}
	return profileColumns
}

// QuoteIdentifier quotes a SQLite identifier.
func QuoteIdentifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
