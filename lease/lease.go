// Package lease coordinates keyed locks across processes using a shared backend.
package lease

import (
	"context"
	"errors"
	"time"
)

// ErrLost indicates that the library can no longer confirm ownership.
var ErrLost = errors.New("lease lost")

// Lease represents a single acquisition. Stop protected work before unlocking.
type Lease interface {
	Unlock(context.Context) error
	Done() <-chan struct{}
	Err() error
}

// Result is the outcome delivered by LockWait and RLockWait.
type Result struct {
	Lease Lease
	Err   error
}

// Lock acquires a mutex or a share of a group.
type Lock interface {
	Lock(context.Context) (Lease, error)
	TryLock(context.Context) (Lease, bool, error)
	LockWait(context.Context) <-chan Result
	// Holders lists the current holders of the key. It is diagnostic: the
	// answer can be stale by the time it returns.
	Holders(context.Context) ([]Holder, error)
}

// Holder is one current holder of a key, as reported by Lock.Holders.
type Holder struct {
	Name string `json:"name"`
	// Mode is "write", "read" or "group".
	Mode  string    `json:"mode"`
	Group string    `json:"group,omitempty"`
	Since time.Time `json:"since"`
}

// LockOptions describe the caller of an acquisition.
type LockOptions struct {
	// Holder names the caller in Holders reports. It is required: printable
	// UTF-8 of at most 128 bytes.
	Holder string
}

// ReadWriteLock admits concurrent readers or an exclusive writer.
type ReadWriteLock interface {
	Lock
	RLock(context.Context) (Lease, error)
	TryRLock(context.Context) (Lease, bool, error)
	RLockWait(context.Context) <-chan Result
}

// Locker creates handles in a backend's configured scope. Keys and groups
// match ^[A-Za-z0-9_-]+$ and are at most 100 characters; acquisition fails
// otherwise.
type Locker interface {
	Mutex(key string, options LockOptions) Lock
	ReadWriteLock(key string, options LockOptions) ReadWriteLock
	GroupLock(key, group string, options LockOptions) Lock
}

// Options configures the coordination scope and bounds individual backend calls.
type Options struct {
	// Scope matches ^[A-Za-z0-9_-]+$ and is at most 100 characters.
	Scope            string
	RetryInterval    time.Duration
	OperationTimeout time.Duration
}

// ExpiringOptions configures a backend that expires abandoned lease records.
type ExpiringOptions struct {
	Options
	// TTL defaults to 30 seconds. Specify whole seconds from 3 seconds to 24 hours.
	TTL time.Duration
}
