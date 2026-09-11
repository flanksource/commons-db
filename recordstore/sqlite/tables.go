package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
)

// Every kind table begins with these two columns, in this order, so a clause
// that has to address the table itself can name them without its schema.
const (
	streamColumn   = "stream_id"
	seqColumn      = "seq"
	catalogVersion = 1
)

func (b *Backend) createCatalog(ctx context.Context) error {
	tx, err := b.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite record store %s: begin catalog: %w", b.path, err)
	}
	defer func() { _ = tx.Rollback() }()
	var versioned bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = 'record_store_format')`).Scan(&versioned); err != nil {
		return fmt.Errorf("sqlite record store %s: inspect catalog: %w", b.path, err)
	}
	if versioned {
		if err := validateCatalog(ctx, tx, b.path); err != nil {
			return err
		}
		return tx.Commit()
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&existing); err != nil {
		return fmt.Errorf("sqlite record store %s: inspect unversioned tables: %w", b.path, err)
	}
	if existing != 0 {
		return fmt.Errorf("sqlite record store %s: unsupported unversioned catalog; remove the file and rebuild it", b.path)
	}
	for _, statement := range []string{
		`CREATE TABLE record_store_format (key INTEGER PRIMARY KEY CHECK (key = 1), version INTEGER NOT NULL)`,
		`INSERT INTO record_store_format (key, version) VALUES (1, 1)`,
		`CREATE TABLE record_streams (
			stream_id TEXT PRIMARY KEY, generation TEXT NOT NULL, kind TEXT NOT NULL, total INTEGER NOT NULL, high_seq INTEGER NOT NULL,
			updated_at TEXT NOT NULL, expires_at TEXT, capped INTEGER NOT NULL DEFAULT 0)`,
		`CREATE INDEX record_streams_expires_at ON record_streams (expires_at)`,
		`CREATE TABLE record_kinds (kind TEXT PRIMARY KEY, table_name TEXT NOT NULL, columns TEXT NOT NULL)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("sqlite record store %s: create catalog: %w", b.path, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite record store %s: commit catalog: %w", b.path, err)
	}
	return nil
}

func validateCatalog(ctx context.Context, tx *sql.Tx, path string) error {
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT version FROM record_store_format WHERE key = 1`).Scan(&version); err != nil {
		return fmt.Errorf("sqlite record store %s: read catalog version: %w", path, err)
	}
	if version != catalogVersion {
		return fmt.Errorf("sqlite record store %s: unsupported catalog version %d, expected %d", path, version, catalogVersion)
	}
	var objects int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE
		(type = 'table' AND name IN ('record_streams', 'record_kinds')) OR
		(type = 'index' AND name = 'record_streams_expires_at')`).Scan(&objects); err != nil {
		return fmt.Errorf("sqlite record store %s: validate catalog: %w", path, err)
	}
	if objects != 3 {
		return fmt.Errorf("sqlite record store %s: catalog version %d is incomplete", path, version)
	}
	return nil
}

// Table is kind's table, created on first use. It is exported for the typed
// result registry, which reads a kind through the table's own columns.
func (b *Backend) Table(kind string) (sqlitetable.Table, error) {
	b.tablesMu.Lock()
	defer b.tablesMu.Unlock()
	if table, ok := b.tables[kind]; ok {
		return table, nil
	}
	columns, err := b.schema(kind)
	if err != nil {
		return sqlitetable.Table{}, fmt.Errorf("kind %q: %w", kind, err)
	}
	for _, column := range columns {
		if column.Name == streamColumn || column.Name == seqColumn {
			return sqlitetable.Table{}, fmt.Errorf("kind %q declares %q, which every stream table reserves", kind, column.Name)
		}
	}
	table := sqlitetable.Table{
		Name: "records_" + kind,
		Columns: append([]query.ColumnDef{
			{Name: streamColumn, Type: query.ColumnTypeString},
			{Name: seqColumn, Type: query.ColumnTypeNumber},
		}, columns...),
		PrimaryKey: []string{streamColumn, seqColumn},
	}
	if err := b.reconcileTable(context.Background(), kind, table); err != nil {
		return sqlitetable.Table{}, err
	}
	b.tables[kind] = table
	return table, nil
}

// storageSignature is what a table's rows depend on: each column's position,
// name and stored type. A label or a filter can change freely; these cannot
// without the rows already written meaning something else.
func storageSignature(table sqlitetable.Table) (string, error) {
	parts := make([]string, len(table.Columns))
	for index, column := range table.Columns {
		parts[index] = column.Name + ":" + sqlitetable.Type(column.Type) + ":" + string(column.Type)
	}
	encoded, err := json.Marshal(parts)
	return string(encoded), err
}

func (b *Backend) reconcileTable(ctx context.Context, kind string, table sqlitetable.Table) error {
	signature, err := storageSignature(table)
	if err != nil {
		return err
	}
	b.mutations.Lock()
	defer b.mutations.Unlock()
	tx, err := b.writeDB.BeginTx(ctx, nil)
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
			kind, b.path, stored, signature)
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

func createKindTable(ctx context.Context, tx *sql.Tx, kind string, table sqlitetable.Table, signature string) error {
	if err := table.Create(ctx, tx); err != nil {
		return fmt.Errorf("kind %q: %w", kind, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO record_kinds (kind, table_name, columns) VALUES (?, ?, ?)`,
		kind, table.Name, signature); err != nil {
		return fmt.Errorf("kind %q: record catalog: %w", kind, err)
	}
	return nil
}

func dropKindTable(ctx context.Context, tx *sql.Tx, kind string, table sqlitetable.Table) error {
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, sqlitetable.QuoteIdentifier(table.Name))); err != nil {
		return fmt.Errorf("kind %q: drop table: %w", kind, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM record_streams WHERE kind = ?`, kind); err != nil {
		return fmt.Errorf("kind %q: drop streams: %w", kind, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM record_kinds WHERE kind = ?`, kind); err != nil {
		return fmt.Errorf("kind %q: drop catalog entry: %w", kind, err)
	}
	return nil
}
