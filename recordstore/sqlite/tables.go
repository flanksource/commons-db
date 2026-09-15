package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/commons/logger"

	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// Every kind table begins with these two columns, in this order, so a clause
// that has to address the table itself can name them without its schema.
const (
	streamColumn   = "stream_id"
	seqColumn      = "seq"
	catalogVersion = 2
)

// kindTable is one kind's table and the schema it was created from.
type kindTable struct {
	sqlitetable.Table
	schema recordstore.KindSchema

	// key is the physical column of the kind's key, or empty for an unkeyed
	// kind.
	key string
}

func (b *Backend) createCatalog(ctx context.Context) error {
	return b.database.Write(func(writer *sql.DB) error {
		return b.createCatalogLocked(ctx, writer)
	})
}

func (b *Backend) createCatalogLocked(ctx context.Context, writer *sql.DB) error {
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite record store %s: begin catalog: %w", b.Path(), err)
	}
	defer func() { _ = tx.Rollback() }()
	err = inspectCatalog(ctx, tx, b.Path())
	var incompatible incompatibleCatalog
	switch {
	case err == nil:
		return tx.Commit()
	case errors.Is(err, errNoCatalog):
	case errors.As(err, &incompatible) && b.derived:
		// Everything a derived index holds is read again from its source, so a
		// file an older build left — a pod restarted onto a new image keeps it —
		// is recreated rather than refused.
		logger.Infof("sqlite record store %s: rebuilding the derived index, which holds %s", b.Path(), incompatible)
		if err := dropTables(ctx, tx, b.Path()); err != nil {
			return err
		}
	case errors.As(err, &incompatible):
		return fmt.Errorf("sqlite record store %s: %s; remove the file and rebuild it", b.Path(), incompatible)
	default:
		return err
	}
	for _, statement := range []string{
		`CREATE TABLE record_store_format (key INTEGER PRIMARY KEY CHECK (key = 1), version INTEGER NOT NULL)`,
		fmt.Sprintf(`INSERT INTO record_store_format (key, version) VALUES (1, %d)`, catalogVersion),
		`CREATE TABLE record_streams (
			stream_id TEXT PRIMARY KEY, generation TEXT NOT NULL, kind TEXT NOT NULL, total INTEGER NOT NULL,
			low_seq INTEGER NOT NULL, high_seq INTEGER NOT NULL,
			updated_at TEXT NOT NULL, expires_at TEXT, capped INTEGER NOT NULL DEFAULT 0)`,
		`CREATE INDEX record_streams_expires_at ON record_streams (expires_at)`,
		`CREATE TABLE record_kinds (kind TEXT PRIMARY KEY, table_name TEXT NOT NULL, columns TEXT NOT NULL)`,
		// record_appends is when each write stored its rows, by the seq of the
		// last row it stored: what Trim finds the rows appended before an
		// instant through.
		`CREATE TABLE record_appends (
			stream_id TEXT NOT NULL, last_seq INTEGER NOT NULL, appended_at TEXT NOT NULL,
			PRIMARY KEY (stream_id, last_seq))`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("sqlite record store %s: create catalog: %w", b.Path(), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite record store %s: commit catalog: %w", b.Path(), err)
	}
	return nil
}

// errNoCatalog reports a file holding no tables at all: a new one.
var errNoCatalog = errors.New("no catalog")

// incompatibleCatalog reports tables this build cannot read — another catalog
// version's, none versioned, or an incomplete set — by what the file holds.
type incompatibleCatalog string

func (c incompatibleCatalog) Error() string { return string(c) }

// inspectCatalog checks the file holds this build's complete catalog. It
// reports errNoCatalog for an empty file and an incompatibleCatalog for tables
// another build wrote.
func inspectCatalog(ctx context.Context, tx *sql.Tx, path string) error {
	var versioned bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = 'record_store_format')`).Scan(&versioned); err != nil {
		return fmt.Errorf("sqlite record store %s: inspect catalog: %w", path, err)
	}
	if !versioned {
		var existing int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&existing); err != nil {
			return fmt.Errorf("sqlite record store %s: inspect unversioned tables: %w", path, err)
		}
		if existing == 0 {
			return errNoCatalog
		}
		return incompatibleCatalog("an unsupported unversioned catalog")
	}
	var version int
	err := tx.QueryRowContext(ctx, `SELECT version FROM record_store_format WHERE key = 1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return incompatibleCatalog("a catalog that records no version")
	}
	if err != nil {
		return fmt.Errorf("sqlite record store %s: read catalog version: %w", path, err)
	}
	if version != catalogVersion {
		return incompatibleCatalog(fmt.Sprintf("an unsupported catalog version %d, expected %d", version, catalogVersion))
	}
	var objects int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE
		(type = 'table' AND name IN ('record_streams', 'record_kinds', 'record_appends')) OR
		(type = 'index' AND name = 'record_streams_expires_at')`).Scan(&objects); err != nil {
		return fmt.Errorf("sqlite record store %s: validate catalog: %w", path, err)
	}
	if objects != 4 {
		return incompatibleCatalog(fmt.Sprintf("an incomplete catalog version %d", version))
	}
	return nil
}

// dropTables removes every table in the file, and the indexes with them.
func dropTables(ctx context.Context, tx *sql.Tx, path string) error {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return fmt.Errorf("sqlite record store %s: list tables to drop: %w", path, err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return fmt.Errorf("sqlite record store %s: list tables to drop: %w", path, err)
		}
		names = append(names, name)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("sqlite record store %s: list tables to drop: %w", path, err)
	}
	for _, name := range names {
		if _, err := tx.ExecContext(ctx, `DROP TABLE "`+strings.ReplaceAll(name, `"`, `""`)+`"`); err != nil {
			return fmt.Errorf("sqlite record store %s: drop table %q: %w", path, name, err)
		}
	}
	return nil
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
			Name: "records_" + kind,
			Columns: append([]query.ColumnDef{
				{Name: streamColumn, Type: query.ColumnTypeString},
				{Name: seqColumn, Type: query.ColumnTypeNumber},
			}, schema.Columns...),
			PrimaryKey: []string{streamColumn, seqColumn},
		},
		schema: schema,
	}
	if schema.Options.Key != "" {
		if table.key, err = table.Physical(schema.Options.Key); err != nil {
			return kindTable{}, err
		}
	}
	if err := b.reconcileTable(context.Background(), kind, table); err != nil {
		return kindTable{}, err
	}
	b.tables[kind] = table
	return table, nil
}

// storageSignature is what a table's rows depend on: each column's position,
// name and stored type, and the key the table holds once per stream. A label
// or a filter can change freely; these cannot without the rows already written
// meaning something else.
func storageSignature(table kindTable) (string, error) {
	parts := make([]string, len(table.Columns), len(table.Columns)+1)
	for index, column := range table.Columns {
		parts[index] = column.Name + ":" + sqlitetable.Type(column.Type) + ":" + string(column.Type)
	}
	if key := table.schema.Options.Key; key != "" {
		parts = append(parts, "key:"+key)
	}
	encoded, err := json.Marshal(parts)
	return string(encoded), err
}

func (b *Backend) reconcileTable(ctx context.Context, kind string, table kindTable) error {
	signature, err := storageSignature(table)
	if err != nil {
		return err
	}
	return b.database.Write(func(writer *sql.DB) error {
		return b.reconcileTableLocked(ctx, writer, kind, table, signature)
	})
}

func (b *Backend) reconcileTableLocked(ctx context.Context, writer *sql.DB, kind string, table kindTable, signature string) error {
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("kind %q: begin: %w", kind, err)
	}
	defer func() { _ = tx.Rollback() }()
	var stored string
	err = tx.QueryRowContext(ctx, `SELECT columns FROM record_kinds WHERE kind = ?`, kind).Scan(&stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := createKindTable(ctx, tx, kind, table, signature); err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("kind %q: read catalog: %w", kind, err)
	case stored == signature:
		return nil
	case !b.derived:
		return fmt.Errorf("kind %q was stored in %s with different columns (%s, now %s); its rows exist nowhere else, so migrate or remove the file",
			kind, b.Path(), stored, signature)
	default:
		if err := dropKindTable(ctx, tx, kind, table); err != nil {
			return err
		}
		if err := createKindTable(ctx, tx, kind, table, signature); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("kind %q: commit: %w", kind, err)
	}
	return nil
}

// createKindTable creates the table, and for a keyed kind the unique index that
// holds each key once per stream — in a derived index too, where the source
// already deduplicated, so an import that would store a key twice fails rather
// than pages a duplicate.
func createKindTable(ctx context.Context, tx *sql.Tx, kind string, table kindTable, signature string) error {
	if err := table.Create(ctx, tx); err != nil {
		return fmt.Errorf("kind %q: %w", kind, err)
	}
	if table.key != "" {
		statement := fmt.Sprintf(`CREATE UNIQUE INDEX %s ON %s ("c0", %s)`,
			sqlitetable.QuoteIdentifier(table.Name+"_key"), sqlitetable.QuoteIdentifier(table.Name), table.key)
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("kind %q: index key: %w", kind, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO record_kinds (kind, table_name, columns) VALUES (?, ?, ?)`,
		kind, table.Name, signature); err != nil {
		return fmt.Errorf("kind %q: record catalog: %w", kind, err)
	}
	return nil
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
