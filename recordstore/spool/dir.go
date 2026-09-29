// Package spool hands batches of stream writes from any process to the one
// that owns a store, as directories of data files committed by rename.
//
// A spool directory holds tmp/ (batches being written), incoming/ (published
// batches, oldest first by name), failed/ (batches that could not be ingested,
// with an error.json) and trash/ (ingested batches awaiting collection). A
// batch is published by writing it under tmp/ and renaming it into incoming/:
// the rename is its only commit point, so a reader never sees half a batch.
package spool

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	tmpDir      = "tmp"
	incomingDir = "incoming"
	failedDir   = "failed"
	trashDir    = "trash"
)

// Dir is a spool directory.
type Dir struct{ path string }

// OpenDir opens the spool directory at path, creating it and its
// subdirectories private to the user.
func OpenDir(path string) (Dir, error) {
	for _, sub := range []string{tmpDir, incomingDir, failedDir, trashDir} {
		if err := os.MkdirAll(filepath.Join(path, sub), 0o700); err != nil {
			return Dir{}, fmt.Errorf("spool %s: %w", path, err)
		}
	}
	return Dir{path: path}, nil
}

// Path is the spool directory.
func (d Dir) Path() string { return d.path }

// Incoming lists the published batches, oldest first.
func (d Dir) Incoming() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(d.path, incomingDir))
	if err != nil {
		return nil, fmt.Errorf("spool %s: list incoming: %w", d.path, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

// Trash moves an ingested batch out of incoming, for Collect to remove. A
// batch trashed before — ingested again after a crash between its commit and
// its move — replaces the earlier copy.
func (d Dir) Trash(name string) error {
	if err := validName(name); err != nil {
		return err
	}
	target := filepath.Join(d.path, trashDir, name)
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("spool %s: clear trash for %s: %w", d.path, name, err)
	}
	if err := os.Rename(filepath.Join(d.path, incomingDir, name), target); err != nil {
		return fmt.Errorf("spool %s: trash %s: %w", d.path, name, err)
	}
	return nil
}

// Fail moves a batch that cannot be ingested to failed, with reason recorded
// beside it in error.json.
func (d Dir) Fail(name string, reason error) error {
	if err := validName(name); err != nil {
		return err
	}
	source := filepath.Join(d.path, incomingDir, name)
	encoded, err := json.Marshal(map[string]any{"error": reason.Error(), "failedAt": time.Now().UTC()})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(source, "error.json"), encoded, 0o600); err != nil {
		return fmt.Errorf("spool %s: record why %s failed: %w", d.path, name, err)
	}
	if err := os.Rename(source, filepath.Join(d.path, failedDir, name)); err != nil {
		return fmt.Errorf("spool %s: move %s to failed: %w", d.path, name, err)
	}
	return nil
}

// CollectOptions say how long abandoned and failed batches are kept.
type CollectOptions struct {
	// TmpAge is how long a batch may stay unpublished in tmp, by its last
	// change, before it is taken as abandoned. Zero is 24 hours.
	TmpAge time.Duration

	// FailedAge is how long a failed batch is kept for inspection. Zero is 30
	// days.
	FailedAge time.Duration
}

// Collect removes every trashed batch, the failed batches older than
// FailedAge and the tmp entries older than TmpAge, as of now.
func (d Dir) Collect(now time.Time, options CollectOptions) error {
	if options.TmpAge == 0 {
		options.TmpAge = 24 * time.Hour
	}
	if options.FailedAge == 0 {
		options.FailedAge = 30 * 24 * time.Hour
	}
	var errs []error
	for sub, age := range map[string]time.Duration{trashDir: 0, failedDir: options.FailedAge, tmpDir: options.TmpAge} {
		entries, err := os.ReadDir(filepath.Join(d.path, sub))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if now.Sub(info.ModTime()) < age {
				continue
			}
			if err := os.RemoveAll(filepath.Join(d.path, sub, entry.Name())); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("spool %s: collect: %w", d.path, err)
	}
	return nil
}

// validName refuses a batch name that is not one entry of a spool
// subdirectory.
func validName(name string) error {
	if name == "" || !filepath.IsLocal(name) || filepath.Base(name) != name {
		return fmt.Errorf("spool: %q is not a batch name", name)
	}
	return nil
}

// syncDir makes the entries of the directory at path durable.
func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
