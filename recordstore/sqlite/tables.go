package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// Every kind table begins with these two columns, in this order. They are
// reserved, so they keep their own names and a kind column deriving an equal
// name is renamed instead.
const (
	streamColumn = "stream_id"
	seqColumn    = "seq"
)

// storeColumns are the columns every kind table begins with.
var storeColumns = []query.ColumnDef{
	{Name: streamColumn, Type: query.ColumnTypeString},
	{Name: seqColumn, Type: query.ColumnTypeNumber},
}

// kindTable is one kind's table and the schema it was created from.
type kindTable struct {
	sqlitetable.Table
	schema recordstore.KindSchema

	// key is the physical column of the kind's key, or empty for an unkeyed
	// kind.
	key string
}

// Table is kind's table, created on first use. It is exported for the typed
// result registry, which reads a kind through the table's own columns.
func (b *Backend) Table(kind string) (sqlitetable.Table, error) {
	table, err := b.kindTable(kind)
	return table.Table, err
}

func (b *Backend) kindTable(kind string) (kindTable, error) {
	b.tablesMu.Lock()
	defer b.tablesMu.Unlock()
	if table, ok := b.tables[kind]; ok {
		return table, nil
	}
	schema, err := recordstore.ResolveKind(b.schema, kind)
	if err != nil {
		return kindTable{}, fmt.Errorf("kind %q: %w", kind, err)
	}
	for _, column := range schema.Columns {
		if column.Name == streamColumn || column.Name == seqColumn {
			return kindTable{}, fmt.Errorf("kind %q declares %q, which every stream table reserves", kind, column.Name)
		}
	}
	table := kindTable{
		Table: sqlitetable.Table{
			Name:       "records_" + kind,
			Columns:    append(slices.Clone(storeColumns), schema.Columns...),
			Reserved:   []string{streamColumn, seqColumn},
			PrimaryKey: []string{streamColumn, seqColumn},
		},
		schema: schema,
	}
	table, err = b.reconcileTable(context.Background(), kind, table)
	if err != nil {
		return kindTable{}, err
	}
	b.tables[kind] = table
	return table, nil
}

// catalogEntries is how record_kinds describes a table: each column's
// declared name, physical name, stored type and declared type, then the key the
// table holds once per stream. A label or a filter can change freely; these
// cannot without the rows already written meaning something else.
func catalogEntries(table kindTable) (string, error) {
	parts := make([]string, len(table.Columns), len(table.Columns)+1)
	for index, column := range table.Columns {
		parts[index] = column.Name + "=" + table.StoredAs[index] + columnStorage(column)
	}
	if key := table.schema.Options.Key; key != "" {
		parts = append(parts, "key:"+key)
	}
	encoded, err := json.Marshal(parts)
	return string(encoded), err
}

// columnStorage is the part of a column's catalog entry after its names.
func columnStorage(column query.ColumnDef) string {
	return ":" + sqlitetable.Type(column.Type) + ":" + string(column.Type)
}

// storedNames reads, from a table's catalog entries, the physical name of each
// of columns in order. It reports false when the entries describe other
// columns or other types.
func storedNames(columns []query.ColumnDef, parts []string) ([]string, bool) {
	if len(parts) != len(columns) {
		return nil, false
	}
	names := make([]string, len(columns))
	for index, column := range columns {
		prefix, suffix := column.Name+"=", columnStorage(column)
		part := parts[index]
		if len(part) <= len(prefix)+len(suffix) || !strings.HasPrefix(part, prefix) || !strings.HasSuffix(part, suffix) {
			return nil, false
		}
		names[index] = part[len(prefix) : len(part)-len(suffix)]
	}
	return names, true
}

// storeTable is kind's table as the catalog records it, holding only the
// columns the store itself addresses: enough to remove or trim a stream's rows
// without resolving the kind's schema.
func storeTable(ctx context.Context, database queryer, kind string) (sqlitetable.Table, error) {
	var name, stored string
	if err := database.QueryRowContext(ctx, `SELECT table_name, columns FROM record_kinds WHERE kind = ?`, kind).Scan(&name, &stored); err != nil {
		return sqlitetable.Table{}, fmt.Errorf("kind %q: read catalog: %w", kind, err)
	}
	var parts []string
	if err := json.Unmarshal([]byte(stored), &parts); err != nil {
		return sqlitetable.Table{}, fmt.Errorf("kind %q: decode catalog columns: %w", kind, err)
	}
	names, ok := storedNames(storeColumns, parts[:min(len(parts), len(storeColumns))])
	if !ok {
		return sqlitetable.Table{}, fmt.Errorf("kind %q: catalog columns %s do not begin with the store's own columns", kind, stored)
	}
	return sqlitetable.Table{Name: name, Columns: storeColumns, StoredAs: names}, nil
}

func (b *Backend) reconcileTable(ctx context.Context, kind string, table kindTable) (kindTable, error) {
	err := b.database.Write(func(writer *sql.DB) error {
		var err error
		table, err = b.reconcileTableLocked(ctx, writer, kind, table)
		return err
	})
	return table, err
}

func (b *Backend) reconcileTableLocked(ctx context.Context, writer *sql.DB, kind string, table kindTable) (kindTable, error) {
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		return kindTable{}, fmt.Errorf("kind %q: begin: %w", kind, err)
	}
	defer func() { _ = tx.Rollback() }()
	var stored string
	err = tx.QueryRowContext(ctx, `SELECT columns FROM record_kinds WHERE kind = ?`, kind).Scan(&stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		table, err = createKindTable(ctx, tx, kind, table)
	case err != nil:
		return kindTable{}, fmt.Errorf("kind %q: read catalog: %w", kind, err)
	default:
		table, err = b.adoptStoredTable(ctx, tx, kind, table, stored)
	}
	if err != nil {
		return kindTable{}, err
	}
	if err := tx.Commit(); err != nil {
		return kindTable{}, fmt.Errorf("kind %q: commit: %w", kind, err)
	}
	return table, nil
}

// adoptStoredTable gives table the physical names its catalog entry recorded,
// once the entry describes the same columns and key and names the columns the
// table actually has. A derived index rebuilds a table that differs; a durable
// file refuses it, because its rows exist nowhere else.
func (b *Backend) adoptStoredTable(ctx context.Context, tx *sql.Tx, kind string, table kindTable, stored string) (kindTable, error) {
	mismatch, err := storedMismatch(ctx, tx, &table, stored)
	if err != nil {
		return kindTable{}, fmt.Errorf("kind %q: %w", kind, err)
	}
	if mismatch == "" {
		return withKey(table)
	}
	if !b.derived {
		return kindTable{}, fmt.Errorf("kind %q was stored in %s with %s; its rows exist nowhere else, so migrate or remove the file", kind, b.Path(), mismatch)
	}
	if err := dropKindTable(ctx, tx, kind, table); err != nil {
		return kindTable{}, err
	}
	table.StoredAs = nil
	return createKindTable(ctx, tx, kind, table)
}

// storedMismatch sets table's physical names from its catalog entry and
// describes how the entry or the table differs from table, or is empty.
func storedMismatch(ctx context.Context, tx *sql.Tx, table *kindTable, stored string) (string, error) {
	var parts []string
	if err := json.Unmarshal([]byte(stored), &parts); err != nil {
		return "", fmt.Errorf("decode catalog columns: %w", err)
	}
	declared := make([]string, len(table.Columns))
	for index, column := range table.Columns {
		declared[index] = column.Name + columnStorage(column)
	}
	if key := table.schema.Options.Key; key != "" {
		declared = append(declared, "key:"+key)
		if len(parts) == 0 || parts[len(parts)-1] != "key:"+key {
			return fmt.Sprintf("different columns (%s, now %q)", stored, declared), nil
		}
		parts = parts[:len(parts)-1]
	}
	names, ok := storedNames(table.Columns, parts)
	if !ok {
		return fmt.Sprintf("different columns (%s, now %q)", stored, declared), nil
	}
	physical, err := sqlitetable.PhysicalColumns(ctx, tx, table.Name)
	if err != nil {
		return "", err
	}
	if !slices.Equal(physical, names) {
		return fmt.Sprintf("catalog columns %q naming a table whose columns are %q", names, physical), nil
	}
	table.StoredAs = names
	return "", nil
}

func withKey(table kindTable) (kindTable, error) {
	if table.schema.Options.Key == "" {
		return table, nil
	}
	var err error
	table.key, err = table.Physical(table.schema.Options.Key)
	return table, err
}

// createKindTable creates the table, and for a keyed kind the unique index that
// holds each key once per stream — in a derived index too, where the source
// already deduplicated, so an import that would store a key twice fails rather
// than pages a duplicate.
func createKindTable(ctx context.Context, tx *sql.Tx, kind string, table kindTable) (kindTable, error) {
	created, err := table.Create(ctx, tx)
	if err != nil {
		return kindTable{}, fmt.Errorf("kind %q: %w", kind, err)
	}
	table.Table = created
	if table, err = withKey(table); err != nil {
		return kindTable{}, err
	}
	if table.key != "" {
		stream, err := table.Physical(streamColumn)
		if err != nil {
			return kindTable{}, err
		}
		statement := fmt.Sprintf(`CREATE UNIQUE INDEX %s ON %s (%s, %s)`,
			sqlitetable.QuoteIdentifier(table.Name+"_key"), sqlitetable.QuoteIdentifier(table.Name), stream, table.key)
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return kindTable{}, fmt.Errorf("kind %q: index key: %w", kind, err)
		}
	}
	entries, err := catalogEntries(table)
	if err != nil {
		return kindTable{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO record_kinds (kind, table_name, columns) VALUES (?, ?, ?)`,
		kind, table.Name, entries); err != nil {
		return kindTable{}, fmt.Errorf("kind %q: record catalog: %w", kind, err)
	}
	return table, nil
}

func dropKindTable(ctx context.Context, tx *sql.Tx, kind string, table kindTable) error {
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, sqlitetable.QuoteIdentifier(table.Name))); err != nil {
		return fmt.Errorf("kind %q: drop table: %w", kind, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM record_appends WHERE stream_id IN (SELECT stream_id FROM record_streams WHERE kind = ?)`, kind); err != nil {
		return fmt.Errorf("kind %q: drop append times: %w", kind, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM record_streams WHERE kind = ?`, kind); err != nil {
		return fmt.Errorf("kind %q: drop streams: %w", kind, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM record_kinds WHERE kind = ?`, kind); err != nil {
		return fmt.Errorf("kind %q: drop catalog entry: %w", kind, err)
	}
	return nil
}
