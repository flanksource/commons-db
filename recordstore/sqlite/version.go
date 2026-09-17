package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/commons/logger"

	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
)

// copiedCatalogVersions are the unversioned catalogs a durable store copies
// into its versioned file on first open: both store columns positionally.
var copiedCatalogVersions = []int{3, 2}

// versionedFile resolves a configured path <dir>/<file> to <dir>/v<catalogVersion>/<file>,
// the file this build opens, creating its directory. A durable store whose
// versioned file does not exist yet copies an older build's unversioned file
// into it, which leaves that file as it was for the builds still reading it; a
// derived index is always built fresh.
func versionedFile(ctx context.Context, path string, derived bool) (string, error) {
	configured, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	parent, err := os.Stat(filepath.Dir(configured))
	if err != nil {
		return "", fmt.Errorf("directory of %s: %w", configured, err)
	}
	versioned := filepath.Join(filepath.Dir(configured), fmt.Sprintf("v%d", catalogVersion), filepath.Base(configured))
	if _, err := os.Stat(versioned); err == nil {
		return versioned, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("inspect %s: %w", versioned, err)
	}
	if err := os.MkdirAll(filepath.Dir(versioned), parent.Mode().Perm()); err != nil {
		return "", fmt.Errorf("create %s: %w", filepath.Dir(versioned), err)
	}
	if derived {
		return versioned, nil
	}
	if _, err := os.Stat(configured); errors.Is(err, fs.ErrNotExist) {
		return versioned, nil
	} else if err != nil {
		return "", fmt.Errorf("inspect %s: %w", configured, err)
	}
	return versioned, copyUnversioned(ctx, configured, versioned)
}

// copyUnversioned copies the unversioned durable file source into target and
// names the copy's columns. The copy is made and migrated under a temporary
// name and linked into place, so a crash leaves no half-migrated target and
// two processes starting together cannot overwrite each other's copy.
func copyUnversioned(ctx context.Context, source, target string) error {
	database, err := sql.Open("sqlite", fileURI(source)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open %s: %w", source, err)
	}
	defer func() { _ = database.Close() }()
	version, err := inspectCatalog(ctx, database, source, copiedCatalogVersions...)
	var incompatible incompatibleCatalog
	switch {
	case errors.Is(err, errNoCatalog):
		return nil
	case errors.As(err, &incompatible):
		return fmt.Errorf("%s holds %s, which cannot be copied into %s; remove it or open it with the build that wrote it", source, incompatible, target)
	case err != nil:
		return err
	}
	// A passive checkpoint moves what it can of the WAL into the file without
	// waiting on another process still using it; VACUUM INTO reads through
	// whatever remains, so the copy is a complete snapshot either way.
	if _, err := database.ExecContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		return fmt.Errorf("checkpoint %s: %w", source, err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".copy-*")
	if err != nil {
		return fmt.Errorf("create a copy of %s: %w", source, err)
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("create a copy of %s: %w", source, err)
	}
	if _, err := database.ExecContext(ctx, `VACUUM INTO ?`, temporary.Name()); err != nil {
		return fmt.Errorf("copy %s: %w", source, err)
	}
	if err := migrateCopy(ctx, temporary.Name(), version); err != nil {
		return fmt.Errorf("migrate the copy of %s: %w", source, err)
	}
	if err := os.Link(temporary.Name(), target); errors.Is(err, fs.ErrExist) {
		logger.Infof("sqlite record store %s: another process copied %s first; using its copy", target, source)
		return nil
	} else if err != nil {
		return fmt.Errorf("place the copy of %s: %w", source, err)
	}
	logger.Infof("sqlite record store %s: copied from %s, which is left as it was for older builds; "+
		"streams either build writes from now on are visible only to that build", target, source)
	return nil
}

// migrateCopy brings the copy at path from version to catalogVersion in one
// transaction: a version 2 catalog gains sealed streams, and every kind table
// renames its positional columns to derived names recorded in record_kinds.
func migrateCopy(ctx context.Context, path string, version int) (err error) {
	database, err := sql.Open("sqlite", fileURI(path))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, database.Close()) }()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if version == 2 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE record_streams ADD COLUMN sealed INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add sealed streams: %w", err)
		}
	}
	kinds, err := storedKinds(ctx, tx)
	if err != nil {
		return err
	}
	for _, kind := range kinds {
		if err := nameKindColumns(ctx, tx, kind); err != nil {
			return fmt.Errorf("kind %q: %w", kind.kind, err)
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE record_store_format SET version = %d WHERE key = 1`, catalogVersion)); err != nil {
		return fmt.Errorf("record catalog version %d: %w", catalogVersion, err)
	}
	return tx.Commit()
}

type storedKind struct{ kind, table, columns string }

func storedKinds(ctx context.Context, tx *sql.Tx) ([]storedKind, error) {
	rows, err := tx.QueryContext(ctx, `SELECT kind, table_name, columns FROM record_kinds ORDER BY kind`)
	if err != nil {
		return nil, fmt.Errorf("read kinds: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var kinds []storedKind
	for rows.Next() {
		var kind storedKind
		if err := rows.Scan(&kind.kind, &kind.table, &kind.columns); err != nil {
			return nil, fmt.Errorf("read kinds: %w", err)
		}
		kinds = append(kinds, kind)
	}
	return kinds, rows.Err()
}

// nameKindColumns renames a positional kind table's columns "c<i>" to the
// names derived from the declared names its version 3 catalog entry recorded,
// and rewrites the entry with them.
func nameKindColumns(ctx context.Context, tx *sql.Tx, kind storedKind) error {
	table, storage, key, err := positionalTable(ctx, tx, kind)
	if err != nil {
		return err
	}
	if table, err = table.Derive(ctx, tx); err != nil {
		return err
	}
	if err := table.RenamePositional(ctx, tx); err != nil {
		return err
	}
	entries := make([]string, len(table.Columns), len(table.Columns)+1)
	for index, column := range table.Columns {
		entries[index] = column.Name + "=" + table.StoredAs[index] + storage[index]
	}
	encoded, err := json.Marshal(append(entries, key...))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE record_kinds SET columns = ? WHERE kind = ?`, string(encoded), kind.kind); err != nil {
		return fmt.Errorf("record named columns: %w", err)
	}
	return nil
}

// positionalTable is a version 3 kind table as its catalog entry describes it:
// its declared columns, each column's stored type part (":SQLTYPE:type") as
// recorded, and the key entry when it has one.
func positionalTable(ctx context.Context, tx *sql.Tx, kind storedKind) (sqlitetable.Table, []string, []string, error) {
	var parts []string
	if err := json.Unmarshal([]byte(kind.columns), &parts); err != nil {
		return sqlitetable.Table{}, nil, nil, fmt.Errorf("decode catalog columns: %w", err)
	}
	physical, err := sqlitetable.PhysicalColumns(ctx, tx, kind.table)
	if err != nil {
		return sqlitetable.Table{}, nil, nil, err
	}
	keyed := len(parts) == len(physical)+1 && strings.HasPrefix(parts[len(parts)-1], "key:")
	if len(parts) != len(physical) && !keyed {
		return sqlitetable.Table{}, nil, nil, fmt.Errorf("catalog columns %s do not describe the %d columns of %q", kind.columns, len(physical), kind.table)
	}
	table := sqlitetable.Table{Name: kind.table, Reserved: []string{streamColumn, seqColumn}}
	storage := make([]string, len(physical))
	for index := range physical {
		typeAt := strings.LastIndex(parts[index], ":")
		storageAt := strings.LastIndex(parts[index][:max(typeAt, 0)], ":")
		if storageAt < 0 {
			return sqlitetable.Table{}, nil, nil, fmt.Errorf("catalog column %q of %q is not name:SQLTYPE:type", parts[index], kind.table)
		}
		table.Columns = append(table.Columns, query.ColumnDef{Name: parts[index][:storageAt], Type: query.ColumnType(parts[index][typeAt+1:])})
		storage[index] = parts[index][storageAt:]
	}
	return table, storage, parts[len(physical):], nil
}

func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}
