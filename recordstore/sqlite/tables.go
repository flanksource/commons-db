package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/flanksource/commons/logger"

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

// columnStorage is the part of a column's catalog entry after its names: its
// stored type and declared type. A label or a filter can change freely; these
// cannot without the rows already written meaning something else.
func columnStorage(column query.ColumnDef) string {
	return ":" + sqlitetable.Type(column.Type) + ":" + string(column.Type)
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
	names := make([]string, len(storeColumns))
	for index, column := range storeColumns {
		var entry storedColumn
		ok := index < len(parts)
		if ok {
			entry, ok = parseStoredColumn(parts[index])
		}
		if !ok || entry.declared != column.Name || entry.storage != columnStorage(column) {
			return sqlitetable.Table{}, fmt.Errorf("kind %q: catalog columns %s do not begin with the store's own columns", kind, stored)
		}
		names[index] = entry.physical
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
// once the entry names the columns the table actually has. A column the kind
// now declares that the table lacks is added in place, in a durable file and a
// derived index alike; a column it no longer declares stays, with its rows.
// Any other difference — a column's storage, the key, a table that has drifted
// from its catalog — would make the rows already written mean something else:
// a derived index rebuilds the table, and a durable file refuses it, because
// its rows exist nowhere else.
func (b *Backend) adoptStoredTable(ctx context.Context, tx *sql.Tx, kind string, table kindTable, stored string) (kindTable, error) {
	catalog, mismatch, err := readCatalog(ctx, tx, table.Name, stored)
	if err != nil {
		return kindTable{}, fmt.Errorf("kind %q: %w", kind, err)
	}
	var added []query.ColumnDef
	if mismatch == "" {
		added, mismatch = table.adopt(catalog)
	}
	switch {
	case mismatch == "" && len(added) == 0:
		return withKey(table)
	case mismatch == "":
		if err := addColumns(ctx, tx, kind, &table, catalog, added); err != nil {
			return kindTable{}, err
		}
		names := make([]string, len(added))
		for index, column := range added {
			names[index] = column.Name
		}
		logger.Infof("sqlite record store %s: kind %q gained columns %q", b.Path(), kind, names)
		return withKey(table)
	case !b.derived:
		return kindTable{}, fmt.Errorf("kind %q was stored in %s with %s; only added columns migrate, and its rows exist nowhere else, so remove the file or open it with the build that wrote it", kind, b.Path(), mismatch)
	}
	if err := dropKindTable(ctx, tx, kind, table); err != nil {
		return kindTable{}, err
	}
	table.StoredAs = nil
	return createKindTable(ctx, tx, kind, table)
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
	entries, err := catalogOf(table).encode()
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
