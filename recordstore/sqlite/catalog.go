package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/commons/logger"
)

// catalogVersion is the catalog this build reads and writes: kind tables whose
// columns carry derived safe names, recorded in record_kinds. A kind table only
// ever gains columns, so its record_kinds entry lists every column it has —
// possibly more than any one build declares — where version 4 listed exactly
// the columns one build declared, and so refused a table another build had
// added a column to.
const catalogVersion = 5

func (b *Backend) createCatalog(ctx context.Context) error {
	return b.database.Write(func(writer *sql.DB) error {
		return b.createCatalogLocked(ctx, writer)
	})
}

// catalogStatements create an empty file's catalog at catalogVersion.
var catalogStatements = []string{
	`CREATE TABLE record_store_format (key INTEGER PRIMARY KEY CHECK (key = 1), version INTEGER NOT NULL)`,
	fmt.Sprintf(`INSERT INTO record_store_format (key, version) VALUES (1, %d)`, catalogVersion),
	`CREATE TABLE record_streams (
		stream_id TEXT PRIMARY KEY, generation TEXT NOT NULL, kind TEXT NOT NULL, total INTEGER NOT NULL,
		low_seq INTEGER NOT NULL, high_seq INTEGER NOT NULL,
		updated_at TEXT NOT NULL, expires_at TEXT, capped INTEGER NOT NULL DEFAULT 0, sealed INTEGER NOT NULL DEFAULT 0)`,
	`CREATE INDEX record_streams_expires_at ON record_streams (expires_at)`,
	`CREATE TABLE record_kinds (kind TEXT PRIMARY KEY, table_name TEXT NOT NULL, columns TEXT NOT NULL)`,
	// record_appends is when each write stored its rows, by the seq of the
	// last row it stored: what Trim finds the rows appended before an
	// instant through.
	`CREATE TABLE record_appends (
		stream_id TEXT NOT NULL, last_seq INTEGER NOT NULL, appended_at TEXT NOT NULL,
		PRIMARY KEY (stream_id, last_seq))`,
}

func (b *Backend) createCatalogLocked(ctx context.Context, writer *sql.DB) error {
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite record store %s: begin catalog: %w", b.Path(), err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = inspectCatalog(ctx, tx, b.Path(), catalogVersion)
	var incompatible incompatibleCatalog
	switch {
	case err == nil:
		return tx.Commit()
	case errors.Is(err, errNoCatalog):
	case errors.As(err, &incompatible) && b.derived:
		// Everything a derived index holds is read again from its source, so a
		// file it cannot read — a pod restarted onto a new image keeps it — is
		// recreated rather than refused.
		logger.Infof("sqlite record store %s: rebuilding the derived index, which holds %s", b.Path(), incompatible)
		if err := dropTables(ctx, tx, b.Path()); err != nil {
			return err
		}
	case errors.As(err, &incompatible):
		return fmt.Errorf("sqlite record store %s: %s; remove the file and rebuild it", b.Path(), incompatible)
	default:
		return err
	}
	for _, statement := range catalogStatements {
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

// inspectCatalog reads the version of the complete catalog the file holds,
// which must be one of supported. It reports errNoCatalog for an empty file
// and an incompatibleCatalog for any other tables.
func inspectCatalog(ctx context.Context, database queryer, path string, supported ...int) (int, error) {
	var versioned bool
	if err := database.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type = 'table' AND name = 'record_store_format')`).Scan(&versioned); err != nil {
		return 0, fmt.Errorf("sqlite record store %s: inspect catalog: %w", path, err)
	}
	if !versioned {
		var existing int
		if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&existing); err != nil {
			return 0, fmt.Errorf("sqlite record store %s: inspect unversioned tables: %w", path, err)
		}
		if existing == 0 {
			return 0, errNoCatalog
		}
		return 0, incompatibleCatalog("an unsupported unversioned catalog")
	}
	var version int
	err := database.QueryRowContext(ctx, `SELECT version FROM record_store_format WHERE key = 1`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, incompatibleCatalog("a catalog that records no version")
	}
	if err != nil {
		return 0, fmt.Errorf("sqlite record store %s: read catalog version: %w", path, err)
	}
	if !slices.Contains(supported, version) {
		return 0, incompatibleCatalog(fmt.Sprintf("an unsupported catalog version %d, expected %s", version, strings.Trim(fmt.Sprint(supported), "[]")))
	}
	var objects int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE
		(type = 'table' AND name IN ('record_streams', 'record_kinds', 'record_appends')) OR
		(type = 'index' AND name = 'record_streams_expires_at')`).Scan(&objects); err != nil {
		return 0, fmt.Errorf("sqlite record store %s: validate catalog: %w", path, err)
	}
	if objects != 4 {
		return 0, incompatibleCatalog(fmt.Sprintf("an incomplete catalog version %d", version))
	}
	return version, nil
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
