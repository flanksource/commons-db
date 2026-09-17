package snapshots

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/flanksource/commons/logger"
)

const snapshotFileName = "snapshot.sqlite"

// snapshotFile is the file this build keeps a snapshot in: under a directory
// named for its metadata version, beside the unversioned file an older build
// kept, so that build still reads its own.
func snapshotFile(dir string) string {
	return filepath.Join(dir, fmt.Sprintf("v%d", snapshotMetadataVersion), snapshotFileName)
}

// snapshotDir is the directory of the snapshot kept in file.
func snapshotDir(file string) string { return filepath.Dir(filepath.Dir(file)) }

// copyV1Snapshot copies the version 1 snapshot an older build left in dir to
// target and names its tables' columns there, leaving the version 1 file as it
// was. The copy is made and migrated under a temporary name and linked into
// place, so a crash never leaves a half-migrated target.
func copyV1Snapshot(ctx context.Context, dir, target string) error {
	legacy := filepath.Join(dir, snapshotFileName)
	if _, err := os.Stat(legacy); err != nil {
		return fmt.Errorf("no snapshot database: %w", err)
	}
	// Snapshot files are written without WAL, so a read-only connection sees
	// every committed row and VACUUM INTO copies them without writing the file.
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(legacy)+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open version 1 snapshot: %w", err)
	}
	defer func() { _ = database.Close() }()
	meta, err := readSnapshotMetadata(ctx, database, 1)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), snapshotFileName+".copy-*")
	if err != nil {
		return fmt.Errorf("copy version 1 snapshot: %w", err)
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("copy version 1 snapshot: %w", err)
	}
	if _, err := database.ExecContext(ctx, `VACUUM INTO ?`, temporary.Name()); err != nil {
		return fmt.Errorf("copy version 1 snapshot: %w", err)
	}
	if err := migrateSnapshotCopy(ctx, temporary.Name(), meta); err != nil {
		return fmt.Errorf("migrate the copy of the version 1 snapshot: %w", err)
	}
	// Another process reloading the same directory can have copied the same v1
	// snapshot and linked it first. Its copy is this copy, so use it rather than
	// fail a reload of a sound snapshot.
	if err := os.Link(temporary.Name(), target); errors.Is(err, fs.ErrExist) {
		logger.Infof("reconciliation snapshot %s: another process copied %s first; using its copy", target, legacy)
		return nil
	} else if err != nil {
		return fmt.Errorf("place the copy of the version 1 snapshot: %w", err)
	}
	logger.Infof("reconciliation snapshot %s: copied from %s, which is left as it was for older builds", target, legacy)
	return nil
}

// migrateSnapshotCopy renames, in one transaction, the positional columns of
// every table the copy at path holds to derived names, and records them in
// the metadata at the current version.
func migrateSnapshotCopy(ctx context.Context, path string, meta snapshotMetadata) (err error) {
	database, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, database.Close()) }()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for index, stored := range meta.Profiles {
		table, err := snapshotTable(stored.Table, stored.Columns).Derive(ctx, tx)
		if err != nil {
			return fmt.Errorf("snapshot profile %q: %w", stored.Name, err)
		}
		if err := table.RenamePositional(ctx, tx); err != nil {
			return fmt.Errorf("snapshot profile %q: %w", stored.Name, err)
		}
		meta.Profiles[index].StoredAs = table.StoredAs
	}
	if err := writeSnapshotMetadataTx(ctx, tx, meta); err != nil {
		return err
	}
	return tx.Commit()
}
