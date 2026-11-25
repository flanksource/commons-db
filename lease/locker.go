package lease

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	// ErrBusy is returned by Acquisition.Try when another holder owns the key.
	ErrBusy = errors.New("lock is busy")
	// ErrNotOwned is returned by Ownership.Renew when the token is no longer active.
	ErrNotOwned = errors.New("ownership token is no longer active")
)

const (
	maxNameLength   = 100
	maxHolderLength = 128
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Mode is how a Request holds its key.
type Mode uint8

const (
	WriteMode Mode = iota
	ReadMode
	GroupMode
)

func (m Mode) String() string {
	switch m {
	case WriteMode:
		return "write"
	case ReadMode:
		return "read"
	default:
		return "group"
	}
}

// Request is one validated acquisition handed to a Backend.
type Request struct {
	Scope, Key, Group, Holder string
	Mode                      Mode
}

// Resource is the readable "<scope>:<key>" name of the requested key.
func (r Request) Resource() string { return r.Scope + ":" + r.Key }

// Ownership is a held acquisition that can be renewed and released.
type Ownership interface {
	Renew(context.Context) error
	Unlock(context.Context) error
}

// Acquisition is one attempt sequence for a Request against a store.
type Acquisition interface {
	Try(context.Context) (Ownership, error)
	Abandon(context.Context) error
}

// RefusedAcquisition fails every attempt with err, for a request a backend
// cannot name.
type RefusedAcquisition struct{ Err error }

func (a RefusedAcquisition) Try(context.Context) (Ownership, error) { return nil, a.Err }
func (RefusedAcquisition) Abandon(context.Context) error            { return nil }

// Backend is a Locker over a lock store: Begin starts an Acquisition for a
// Request and Holders (optional) reports who holds a key. Store packages such
// as lease/redis and lease/kubernetes build one after NormalizeExpiring.
type Backend struct {
	Options Options
	TTL     time.Duration
	Begin   func(Request, bool) Acquisition
	Holders func(context.Context, Request) ([]Holder, error)
}

func validateName(field, value string) error {
	if len(value) > maxNameLength || !namePattern.MatchString(value) {
		return fmt.Errorf("lease %s %q must match %s and be at most %d characters", field, value, namePattern, maxNameLength)
	}
	return nil
}

func validateHolder(holder string) error {
	if holder == "" || len(holder) > maxHolderLength || !utf8.ValidString(holder) || strings.IndexFunc(holder, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return fmt.Errorf("lease holder %q must be nonempty printable UTF-8 of at most %d bytes", holder, maxHolderLength)
	}
	return nil
}

func normalize(options Options) (Options, error) {
	if err := validateName("scope", options.Scope); err != nil {
		return options, err
	}
	if options.RetryInterval < 0 || options.OperationTimeout < 0 {
		return options, errors.New("lease retry interval and operation timeout must be positive")
	}
	if options.RetryInterval == 0 {
		options.RetryInterval = 200 * time.Millisecond
	}
	if options.OperationTimeout == 0 {
		options.OperationTimeout = 5 * time.Second
	}
	return options, nil
}

func NormalizeExpiring(options ExpiringOptions) (ExpiringOptions, error) {
	base, err := normalize(options.Options)
	if err != nil {
		return options, err
	}
	options.Options = base
	if options.TTL == 0 {
		options.TTL = 30 * time.Second
	}
	if options.TTL < 3*time.Second || options.TTL > 24*time.Hour || options.TTL%time.Second != 0 {
		return options, errors.New("lease TTL must be whole seconds from 3 seconds through 24 hours")
	}
	if options.RetryInterval > options.TTL/3 {
		return options, errors.New("lease retry interval cannot exceed one third of the TTL")
	}
	return options, nil
}

func (l *Backend) Mutex(key string, options LockOptions) Lock {
	return lock{locker: l, key: key, holder: options.Holder}
}

func (l *Backend) ReadWriteLock(key string, options LockOptions) ReadWriteLock {
	return lock{locker: l, key: key, holder: options.Holder}
}

func (l *Backend) GroupLock(key, group string, options LockOptions) Lock {
	return lock{locker: l, key: key, group: group, holder: options.Holder, mode: GroupMode}
}

type lock struct {
	locker             *Backend
	key, group, holder string
	mode               Mode
}

func (l lock) Lock(ctx context.Context) (Lease, error) { return l.acquire(ctx, true) }

func (l lock) TryLock(ctx context.Context) (Lease, bool, error) {
	lease, err := l.acquire(ctx, false)
	if err == ErrBusy {
		return nil, false, nil
	}
	return lease, err == nil, err
}

func (l lock) LockWait(ctx context.Context) <-chan Result {
	result := make(chan Result)
	go func() {
		defer close(result)
		held, err := l.Lock(ctx)
		if held != nil && ctx.Err() == nil {
			select {
			case result <- Result{Lease: held}:
				return
			case <-ctx.Done():
			case <-held.Done():
				err = held.Err()
			}
		}
		if held != nil {
			err = errors.Join(err, held.Unlock(ctx))
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		select {
		case result <- Result{Err: err}:
		case <-ctx.Done():
		}
	}()
	return result
}

func (l lock) RLock(ctx context.Context) (Lease, error) {
	l.mode = ReadMode
	return l.Lock(ctx)
}

func (l lock) TryRLock(ctx context.Context) (Lease, bool, error) {
	l.mode = ReadMode
	return l.TryLock(ctx)
}

func (l lock) RLockWait(ctx context.Context) <-chan Result {
	l.mode = ReadMode
	return l.LockWait(ctx)
}

func (l lock) Holders(ctx context.Context) ([]Holder, error) {
	req, err := l.request()
	if err != nil {
		return nil, err
	}
	if l.locker.Holders == nil {
		return nil, errors.New("this lease backend does not report holders")
	}
	operation, cancel := context.WithTimeout(ctx, l.locker.operationTimeout())
	defer cancel()
	holders, err := l.locker.Holders(operation, req)
	if err != nil {
		return nil, fmt.Errorf("list holders of lease %s: %w", req.Resource(), err)
	}
	slices.SortFunc(holders, func(a, b Holder) int {
		return cmp.Or(a.Since.Compare(b.Since), strings.Compare(a.Name, b.Name))
	})
	return holders, nil
}

func (l lock) request() (Request, error) {
	req := Request{Scope: l.locker.Options.Scope, Key: l.key, Holder: l.holder, Mode: l.mode}
	err := validateName("key", l.key)
	if l.mode == GroupMode {
		req.Group = l.group
		err = errors.Join(err, validateName("group", l.group))
	}
	return req, errors.Join(err, validateHolder(l.holder))
}

func (l lock) acquire(ctx context.Context, wait bool) (Lease, error) {
	req, err := l.request()
	if err != nil {
		return nil, err
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return l.locker.acquire(ctx, req, wait)
}

func (l *Backend) acquire(ctx context.Context, req Request, wait bool) (_ Lease, err error) {
	attempt := l.Begin(req, wait)
	defer func() {
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.Options.OperationTimeout)
			defer cancel()
			if cleanupErr := attempt.Abandon(cleanup); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("clean up acquisition: %w", cleanupErr))
			}
		}
	}()
	for {
		start := time.Now()
		operation, cancel := context.WithTimeout(ctx, l.operationTimeout())
		held, err := attempt.Try(operation)
		operationErr := context.Cause(operation)
		cancel()
		if held != nil {
			lease := l.manage(ctx, held, start)
			if cause := context.Cause(ctx); cause != nil {
				return nil, errors.Join(cause, lease.Unlock(ctx))
			}
			if operationErr != nil {
				return nil, errors.Join(operationErr, lease.Unlock(ctx))
			}
			return lease, nil
		}
		if cause := context.Cause(ctx); cause != nil {
			return nil, cause
		}
		if err != ErrBusy || !wait {
			return nil, err
		}
		if err := Pause(ctx, l.Options.RetryInterval); err != nil {
			return nil, err
		}
	}
}

func (l *Backend) operationTimeout() time.Duration {
	if l.TTL == 0 {
		return l.Options.OperationTimeout
	}
	return min(l.Options.OperationTimeout, l.TTL/3)
}

func Pause(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval - time.Duration(rand.Int64N(max(1, int64(interval/4)))))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

func Digest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(h, "%d:%s", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))
}
