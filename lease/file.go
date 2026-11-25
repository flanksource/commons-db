package lease

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

var errGroupsUnsupported = errors.New("group locks are not supported by the file backend")

// NewFile coordinates the processes of one host through flock files under
// dir: <dir>/<digest>.lock holds the lock and <dir>/<digest>.intent queues a
// waiting writer ahead of new readers. Each holder also keeps a record in
// <dir>/<digest>.holders/, flocked for the life of its lease, which Holders
// reads back. A lease lasts until it is unlocked or its process exits and is
// never lost. GroupLock is refused.
func NewFile(dir string, options Options) (Locker, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("file lock directory is required")
	}
	options, err := normalize(options)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create file lock directory %s: %w", dir, err)
	}
	paths := func(req Request) string { return filepath.Join(dir, Digest(req.Scope, req.Key)) }
	return &Backend{Options: options,
		Begin: func(req Request, wait bool) Acquisition {
			if req.Mode == GroupMode {
				return RefusedAcquisition{errGroupsUnsupported}
			}
			base := paths(req)
			return &fileAcquisition{base: base, holder: req.Holder, mode: req.Mode, waiting: wait}
		},
		Holders: func(_ context.Context, req Request) ([]Holder, error) { return fileHolders(paths(req) + ".holders") },
	}, nil
}

type fileAcquisition struct {
	base, holder string
	mode         Mode
	waiting      bool
	queued       *flock.Flock
}

func (a *fileAcquisition) Try(context.Context) (Ownership, error) {
	var held *flock.Flock
	var err error
	switch {
	case a.mode == ReadMode:
		held, err = a.tryRead()
	case a.waiting:
		held, err = a.tryQueuedWrite()
	default:
		held, err = tryFileLock(flock.New(a.base+".lock"), (*flock.Flock).TryLock)
	}
	if err != nil {
		return nil, err
	}
	record, path, err := writeHolderRecord(a.base+".holders", Holder{Name: a.holder, Mode: a.mode.String(), Since: time.Now().UTC()})
	if err != nil {
		return nil, errors.Join(err, held.Unlock())
	}
	return fileOwnership{lock: held, record: record, recordPath: path}, nil
}

// tryRead passes the intent file shared, so a queued writer turns it away,
// then shares the lock.
func (a *fileAcquisition) tryRead() (*flock.Flock, error) {
	gate := flock.New(a.base + ".intent")
	if _, err := tryFileLock(gate, (*flock.Flock).TryRLock); err != nil {
		return nil, err
	}
	held, err := tryFileLock(flock.New(a.base+".lock"), (*flock.Flock).TryRLock)
	if unlockErr := gate.Unlock(); unlockErr != nil {
		if held != nil {
			unlockErr = errors.Join(unlockErr, held.Unlock())
		}
		return nil, errors.Join(err, unlockErr)
	}
	return held, err
}

// tryQueuedWrite holds the intent file exclusively across attempts, which
// stops new readers, until the lock is acquired or the attempt is abandoned.
func (a *fileAcquisition) tryQueuedWrite() (*flock.Flock, error) {
	if a.queued == nil {
		gate := flock.New(a.base + ".intent")
		if _, err := tryFileLock(gate, (*flock.Flock).TryLock); err != nil {
			return nil, err
		}
		a.queued = gate
	}
	held, err := tryFileLock(flock.New(a.base+".lock"), (*flock.Flock).TryLock)
	if err != nil {
		return nil, err
	}
	if err := a.Abandon(context.Background()); err != nil {
		return nil, errors.Join(err, held.Unlock())
	}
	return held, nil
}

func (a *fileAcquisition) Abandon(context.Context) error {
	if a.queued == nil {
		return nil
	}
	err := a.queued.Unlock()
	a.queued = nil
	return err
}

func tryFileLock(lock *flock.Flock, try func(*flock.Flock) (bool, error)) (*flock.Flock, error) {
	locked, err := try(lock)
	if err != nil {
		return nil, fmt.Errorf("file lock %s: %w", lock.Path(), err)
	}
	if !locked {
		return nil, ErrBusy
	}
	return lock, nil
}

// fileOwnership is held by open file descriptors, so it cannot be lost.
type fileOwnership struct {
	lock, record *flock.Flock
	recordPath   string
}

func (fileOwnership) Renew(context.Context) error { return nil }

func (h fileOwnership) Unlock(context.Context) error {
	err := os.Remove(h.recordPath)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	return errors.Join(err, h.record.Unlock(), h.lock.Unlock())
}

// writeHolderRecord writes the holder under a temporary name, flocks it
// shared and only then renames it into dir, so a listed record is always
// held while its holder lives.
func writeHolderRecord(dir string, holder Holder) (*flock.Flock, string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", fmt.Errorf("create lease holder records %s: %w", dir, err)
	}
	data, err := json.Marshal(holder)
	if err != nil {
		return nil, "", err
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return nil, "", err
	}
	name := hex.EncodeToString(token)
	temporary := filepath.Join(dir, "."+name)
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		return nil, "", fmt.Errorf("write lease holder record: %w", err)
	}
	record, err := tryFileLock(flock.New(temporary), (*flock.Flock).TryRLock)
	if err != nil {
		return nil, "", errors.Join(err, os.Remove(temporary))
	}
	final := filepath.Join(dir, name)
	if err := os.Rename(temporary, final); err != nil {
		return nil, "", errors.Join(fmt.Errorf("publish lease holder record: %w", err), record.Unlock(), os.Remove(temporary))
	}
	return record, final, nil
}

// fileHolders reads the records in dir. A record no live holder flocks is
// left by a process that exited, and is removed.
func fileHolders(dir string) ([]Holder, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []Holder{}, nil
	}
	if err != nil {
		return nil, err
	}
	holders := []Holder{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		probe := flock.New(path)
		stale, err := probe.TryLock()
		if err != nil {
			return nil, fmt.Errorf("probe lease holder record %s: %w", path, err)
		}
		if stale {
			if err := errors.Join(os.Remove(path), probe.Unlock()); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
			continue
		}
		var holder Holder
		if err := json.Unmarshal(data, &holder); err != nil {
			return nil, fmt.Errorf("read lease holder record %s: %w", path, err)
		}
		holders = append(holders, holder)
	}
	return holders, nil
}
