// The lock that elects a store's owner, and Exclusive, which holds it for a
// backend with no read-only mode to fall back to.
package owner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// ErrLocked reports a store another process holds.
var ErrLocked = errors.New("record store is held by another process")

// LockedError is ErrLocked for the store at Path, naming the process holding
// it when that process published its state.
type LockedError struct {
	Path  string
	Owner *State
}

func (e *LockedError) Error() string {
	if e.Owner == nil {
		return fmt.Sprintf("%s: %s", e.Path, ErrLocked)
	}
	return fmt.Sprintf("%s: %s: pid %d on %s, build %q, instance %s", e.Path, ErrLocked, e.Owner.PID, e.Owner.Host, e.Owner.Build, e.Owner.Instance)
}

func (e *LockedError) Unwrap() error { return ErrLocked }

// lockedBy is the LockedError for configured, naming its holder if it can.
func lockedBy(path, configured string) error {
	state, found, err := ReadState(configured)
	if err != nil || !found {
		return &LockedError{Path: path}
	}
	return &LockedError{Path: path, Owner: &state}
}

// prepare makes the directory of configured and refuses a filesystem the lock
// cannot be trusted on.
func prepare(configured string) error {
	dir := filepath.Dir(configured)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("record store owner: create %s: %w", dir, err)
	}
	return RefuseNetworkFilesystem(dir)
}

// tryLock takes the lock of the store at configured if it is free.
func tryLock(configured string) (*flock.Flock, bool, error) {
	lock := flock.New(lockPath(configured))
	held, err := lock.TryLock()
	if err != nil {
		return nil, false, fmt.Errorf("record store owner: lock %s: %w", configured, err)
	}
	if !held {
		return nil, false, lock.Close()
	}
	return lock, true, nil
}

// Exclusive holds the store at path for this process, publishing its state so
// another process refused with a *LockedError can name it, and returns the
// function that releases it. It is for a store only one process can open at
// all, such as a directory of ndjson files.
func Exclusive(path, build string) (release func() error, err error) {
	configured, err := absolute(path)
	if err != nil {
		return nil, err
	}
	if err := prepare(configured); err != nil {
		return nil, err
	}
	lock, held, err := tryLock(configured)
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, lockedBy(path, configured)
	}
	instance := newInstance()
	if err := writeState(configured, processState(instance, build, PhaseReady)); err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	return func() error {
		return errors.Join(removeState(configured, instance), lock.Close())
	}, nil
}
