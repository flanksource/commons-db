package lease

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

var (
	errBusy     = errors.New("lock is busy")
	errNotOwned = errors.New("ownership token is no longer active")
)

type mode uint8

const (
	writeMode mode = iota
	readMode
	groupMode
)

type request struct {
	key, group string
	mode       mode
}

type ownership interface {
	renew(context.Context) error
	unlock(context.Context) error
}

type acquisition interface {
	try(context.Context) (ownership, error)
	abandon(context.Context) error
}

type locker struct {
	options Options
	ttl     time.Duration
	begin   func(request, bool) acquisition
}

func normalize(options Options) (Options, error) {
	if strings.TrimSpace(options.Scope) == "" {
		return options, errors.New("lease scope is required")
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

func (l *locker) Mutex(key string) Lock                  { return lock{locker: l, key: key} }
func (l *locker) ReadWriteLock(key string) ReadWriteLock { return lock{locker: l, key: key} }
func (l *locker) GroupLock(key, group string) Lock {
	return lock{locker: l, key: key, group: group, mode: groupMode}
}

type lock struct {
	locker     *locker
	key, group string
	mode       mode
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

func (l lock) acquire(ctx context.Context, wait bool) (Lease, error) {
	if strings.TrimSpace(l.key) == "" || l.mode == groupMode && strings.TrimSpace(l.group) == "" {
		return nil, errors.New("lease key and group must be nonempty")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	req := request{key: digest(l.locker.options.Scope, l.key), mode: l.mode}
	if l.mode == groupMode {
		req.group = digest(l.group)
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
