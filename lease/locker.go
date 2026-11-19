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
	errBusy     = errors.New("lock is busy")
	errNotOwned = errors.New("ownership token is no longer active")
)

const (
	maxNameLength   = 100
	maxHolderLength = 128
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type mode uint8

const (
	writeMode mode = iota
	readMode
	groupMode
)

func (m mode) String() string {
	switch m {
	case writeMode:
		return "write"
	case readMode:
		return "read"
	default:
		return "group"
	}
}

type request struct {
	scope, key, group, holder string
	mode                      mode
}

// resource is the readable "<scope>:<key>" name of the requested key.
func (r request) resource() string { return r.scope + ":" + r.key }

type ownership interface {
	renew(context.Context) error
	unlock(context.Context) error
}

type acquisition interface {
	try(context.Context) (ownership, error)
	abandon(context.Context) error
}

// refusedAcquisition fails every attempt with err, for a request a backend
// cannot name.
type refusedAcquisition struct{ err error }

func (a refusedAcquisition) try(context.Context) (ownership, error) { return nil, a.err }
func (refusedAcquisition) abandon(context.Context) error            { return nil }

type locker struct {
	options Options
	ttl     time.Duration
	begin   func(request, bool) acquisition
	holders func(context.Context, request) ([]Holder, error)
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

func normalizeExpiring(options ExpiringOptions) (ExpiringOptions, error) {
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

func (l *locker) Mutex(key string, options LockOptions) Lock {
	return lock{locker: l, key: key, holder: options.Holder}
}

func (l *locker) ReadWriteLock(key string, options LockOptions) ReadWriteLock {
	return lock{locker: l, key: key, holder: options.Holder}
}

func (l *locker) GroupLock(key, group string, options LockOptions) Lock {
	return lock{locker: l, key: key, group: group, holder: options.Holder, mode: groupMode}
}

type lock struct {
	locker             *locker
	key, group, holder string
	mode               mode
}

func (l lock) Lock(ctx context.Context) (Lease, error) { return l.acquire(ctx, true) }

func (l lock) TryLock(ctx context.Context) (Lease, bool, error) {
	lease, err := l.acquire(ctx, false)
	if err == errBusy {
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
	l.mode = readMode
	return l.Lock(ctx)
}

func (l lock) TryRLock(ctx context.Context) (Lease, bool, error) {
	l.mode = readMode
	return l.TryLock(ctx)
}

func (l lock) RLockWait(ctx context.Context) <-chan Result {
	l.mode = readMode
	return l.LockWait(ctx)
}

func (l lock) Holders(ctx context.Context) ([]Holder, error) {
	req, err := l.request()
	if err != nil {
		return nil, err
	}
	if l.locker.holders == nil {
		return nil, errors.New("this lease backend does not report holders")
	}
	operation, cancel := context.WithTimeout(ctx, l.locker.operationTimeout())
	defer cancel()
	holders, err := l.locker.holders(operation, req)
	if err != nil {
		return nil, fmt.Errorf("list holders of lease %s: %w", req.resource(), err)
	}
	slices.SortFunc(holders, func(a, b Holder) int {
		return cmp.Or(a.Since.Compare(b.Since), strings.Compare(a.Name, b.Name))
	})
	return holders, nil
}

func (l lock) request() (request, error) {
	req := request{scope: l.locker.options.Scope, key: l.key, holder: l.holder, mode: l.mode}
	err := validateName("key", l.key)
	if l.mode == groupMode {
		req.group = l.group
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

func (l *locker) acquire(ctx context.Context, req request, wait bool) (_ Lease, err error) {
	attempt := l.begin(req, wait)
	defer func() {
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), l.options.OperationTimeout)
			defer cancel()
			if cleanupErr := attempt.abandon(cleanup); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("clean up acquisition: %w", cleanupErr))
			}
		}
	}()
	for {
		start := time.Now()
		operation, cancel := context.WithTimeout(ctx, l.operationTimeout())
		held, err := attempt.try(operation)
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
		if err != errBusy || !wait {
			return nil, err
		}
		if err := pause(ctx, l.options.RetryInterval); err != nil {
			return nil, err
		}
	}
}

func (l *locker) operationTimeout() time.Duration {
	if l.ttl == 0 {
		return l.options.OperationTimeout
	}
	return min(l.options.OperationTimeout, l.ttl/3)
}

func pause(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval - time.Duration(rand.Int64N(max(1, int64(interval/4)))))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

func digest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(h, "%d:%s", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))
}
