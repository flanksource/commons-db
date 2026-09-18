// Package sqlitetable writes rows described by query.ColumnDef into SQLite
// tables that a `sql` profile can read back under the declared column names.
//
// Each column is stored under a safe physical name derived from its declared
// one when the table is created (see PhysicalNames), and aliased back on the
// way out, so any string a profile can name a column is a name this package
// can store, while the table stays readable by name in hand-written SQL. The
// derived names are the table's own: its owner persists Table.StoredAs and
// hands it back, because a later build deriving differently must never
// reinterpret an existing table.
package sqlitetable

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"ariga.io/atlas/sql/schema"

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

	// StoredAs is the physical column each of Columns is stored in, index-aligned
	// with Columns. Create derives it for a table created without it; a table
	// read back from storage carries the names it was created with.
	StoredAs []string

	// Reserved names the declared columns the table's owner addresses by their
	// own names. Each keeps its name, which must already be a safe bare name, and
	// every other column deriving an equal name is numbered instead.
	Reserved []string

	// PrimaryKey names the declared columns that together identify a row.
	PrimaryKey []string

	// Unique names declared columns that each get their own unique index.
	Unique []string
}

// Write creates the table, inserts rows into it, and returns it with the
// physical names it was created with.
func Write(ctx context.Context, database Execer, table Table, rows []query.Row) (Table, error) {
	created, err := table.Create(ctx, database)
	if err != nil {
		return Table{}, err
	}
	return created, created.Insert(ctx, database, rows)
}

// Create creates the table and its unique indexes, and returns it with the
// physical names it was created with: StoredAs as given, or derived on
// database when empty. The table must not exist.
func (t Table) Create(ctx context.Context, database Execer) (Table, error) {
	if len(t.StoredAs) == 0 {
		derived, err := t.Derive(ctx, database)
		if err != nil {
			return Table{}, err
		}
		t = derived
	}
	if err := t.checkStoredAs(); err != nil {
		return Table{}, err
	}
	definitions := make([]string, len(t.Columns), len(t.Columns)+1)
	for index, column := range t.Columns {
		definitions[index] = QuoteIdentifier(t.StoredAs[index]) + " " + Type(column.Type)
	}
	if len(t.PrimaryKey) > 0 {
		keys, err := t.physicalList(t.PrimaryKey)
		if err != nil {
			return Table{}, err
		}
		definitions = append(definitions, "PRIMARY KEY ("+keys+")")
	}
	statement := fmt.Sprintf(`CREATE TABLE %s (%s)`, QuoteIdentifier(t.Name), strings.Join(definitions, ", "))
	if _, err := database.ExecContext(ctx, statement); err != nil {
		return Table{}, fmt.Errorf("create table %q: %w", t.Name, err)
	}
	for _, name := range t.Unique {
		physical, err := t.Physical(name)
		if err != nil {
			return Table{}, err
		}
		statement := fmt.Sprintf(`CREATE UNIQUE INDEX %s ON %s (%s)`,
			QuoteIdentifier(t.Name+"_"+name), QuoteIdentifier(t.Name), physical)
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return Table{}, fmt.Errorf("index table %q: %w", t.Name, err)
		}
	}
	return t, nil
}

// Derive returns the table with StoredAs derived on database by PhysicalNames:
// reserved columns keep their names and every other column is derived against
// them. It is for a table being created, or migrated from positional storage;
// an existing table's names are its stored ones.
func (t Table) Derive(ctx context.Context, database Execer) (Table, error) {
	bare := Bare(ctx, database)
	reserved, err := PhysicalNames(t.Reserved, nil, bare)
	if err != nil {
		return Table{}, fmt.Errorf("table %q: %w", t.Name, err)
	}
	for index, name := range t.Reserved {
		if reserved[index] != name {
			return Table{}, fmt.Errorf("table %q: reserved column %q is not a safe bare name", t.Name, name)
		}
	}
	var declared []string
	for _, column := range t.Columns {
		if !slices.Contains(t.Reserved, column.Name) {
			declared = append(declared, column.Name)
		}
	}
	physical, err := PhysicalNames(declared, t.Reserved, bare)
	if err != nil {
		return Table{}, fmt.Errorf("table %q: %w", t.Name, err)
	}
	t.StoredAs = make([]string, len(t.Columns))
	for index, column := range t.Columns {
		if slices.Contains(t.Reserved, column.Name) {
			t.StoredAs[index] = column.Name
			continue
		}
		t.StoredAs[index], physical = physical[0], physical[1:]
	}
	return t, nil
}

// Declare is the table as Create creates it, declared for Atlas: every column
// under its stored name by its raw SQLite type, the primary key and the unique
// indexes. migrate/sqlite.Apply reconciles an existing table against it. The
// table must carry its stored names.
func (t Table) Declare() (*schema.Table, error) {
	if err := t.checkStoredAs(); err != nil {
		return nil, err
	}
	declared := schema.NewTable(t.Name)
	for index, column := range t.Columns {
		declared.AddColumns(&schema.Column{Name: t.StoredAs[index], Type: &schema.ColumnType{Raw: Type(column.Type), Null: true}})
	}
	stored := func(name string) (*schema.Column, error) {
		index := slices.IndexFunc(t.Columns, func(column query.ColumnDef) bool { return column.Name == name })
		if index < 0 {
			return nil, fmt.Errorf("table %q has no column %q", t.Name, name)
		}
		return declared.Columns[index], nil
	}
	if len(t.PrimaryKey) > 0 {
		keys := make([]*schema.Column, len(t.PrimaryKey))
		for index, name := range t.PrimaryKey {
			column, err := stored(name)
			if err != nil {
				return nil, err
			}
			keys[index] = column
		}
		declared.SetPrimaryKey(schema.NewPrimaryKey(keys...))
	}
	for _, name := range t.Unique {
		column, err := stored(name)
		if err != nil {
			return nil, err
		}
		declared.AddIndexes(schema.NewUniqueIndex(t.Name + "_" + name).AddColumns(column))
	}
	return declared, nil
}

func (t Table) checkStoredAs() error {
	if len(t.StoredAs) != len(t.Columns) {
		return fmt.Errorf("table %q has %d stored names for %d columns", t.Name, len(t.StoredAs), len(t.Columns))
	}
	return nil
}

// Insert appends rows to the existing table. A row key the table does not
// declare is not stored; a declared column the row lacks is stored as NULL, as
// is any column the table has but does not declare — one another build sharing
// the file added.
func (t Table) Insert(ctx context.Context, database Execer, rows []query.Row) error {
	if len(rows) == 0 {
		return nil
	}
	if err := t.checkStoredAs(); err != nil {
		return err
	}
	columns := make([]string, len(t.StoredAs))
	for index, name := range t.StoredAs {
		columns[index] = QuoteIdentifier(name)
	}
	markers := strings.TrimRight(strings.Repeat("?,", len(t.Columns)), ",")
	statement, err := database.PrepareContext(ctx, fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`,
		QuoteIdentifier(t.Name), strings.Join(columns, ", "), markers))
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

// Select reads every declared column under its declared name, aliasing only
// the columns stored under another name. It panics on a table that carries no
// stored names: that table was neither created nor read back from storage.
func (t Table) Select() string {
	if err := t.checkStoredAs(); err != nil {
		panic(err)
	}
	selects := make([]string, len(t.Columns))
	for index, column := range t.Columns {
		selects[index] = QuoteIdentifier(t.StoredAs[index])
		if t.StoredAs[index] != column.Name {
			selects[index] += " AS " + QuoteIdentifier(column.Name)
		}
	}
	return fmt.Sprintf(`SELECT %s FROM %s`, strings.Join(selects, ", "), QuoteIdentifier(t.Name))
}

// Physical is the quoted column a declared column is stored in, for a clause
// that has to address the table itself rather than the aliased result.
func (t Table) Physical(name string) (string, error) {
	if err := t.checkStoredAs(); err != nil {
		return "", err
	}
	index := slices.IndexFunc(t.Columns, func(column query.ColumnDef) bool { return column.Name == name })
	if index < 0 {
		return "", fmt.Errorf("table %q has no column %q", t.Name, name)
	}
	return QuoteIdentifier(t.StoredAs[index]), nil
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
