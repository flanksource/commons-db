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
}

// ReadWriteLock admits concurrent readers or an exclusive writer.
type ReadWriteLock interface {
	Lock
	RLock(context.Context) (Lease, error)
	TryRLock(context.Context) (Lease, bool, error)
	RLockWait(context.Context) <-chan Result
}

// Locker creates handles in a backend's configured scope.
type Locker interface {
	Mutex(key string) Lock
	ReadWriteLock(key string) ReadWriteLock
	GroupLock(key, group string) Lock
}

// Options configures the coordination scope and bounds individual backend calls.
type Options struct {
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
