package lease

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type managedLease struct {
	done, cleaned  chan struct{}
	stop           context.CancelFunc
	finish         sync.Once
	err, unlockErr error
}

func (l *managedLease) Done() <-chan struct{} { return l.done }

func (l *managedLease) Err() error {
	select {
	case <-l.done:
		return l.err
	default:
		return nil
	}
}

func (l *managedLease) end(err error) {
	l.finish.Do(func() {
		l.err = err
		close(l.done)
		l.stop()
	})
}

func (l *managedLease) Unlock(context.Context) error {
	l.end(nil)
	<-l.cleaned
	return l.unlockErr
}

func (l *locker) manage(ctx context.Context, held ownership, start time.Time) *managedLease {
	run, cancel := context.WithCancel(context.WithoutCancel(ctx))
	lease := &managedLease{done: make(chan struct{}), cleaned: make(chan struct{}), stop: cancel}
	go func() {
		defer close(lease.cleaned)
		if err := l.maintain(run, held, start); err != nil {
			lease.end(fmt.Errorf("%w: %w", ErrLost, err))
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(run), l.options.OperationTimeout)
		defer cancel()
		if err := held.unlock(cleanup); err != nil && !errors.Is(err, errNotOwned) {
			lease.unlockErr = err
		}
	}()
	return lease
}

func (l *locker) maintain(ctx context.Context, held ownership, last time.Time) error {
	interval := 10 * time.Second
	if l.ttl != 0 {
		interval = l.ttl / 3
	}
	next := time.Until(last.Add(interval))
	for {
		timer := time.NewTimer(next)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		start := time.Now()
		deadline := start.Add(l.operationTimeout())
		if l.ttl != 0 {
			deadline = minTime(deadline, last.Add(l.ttl/3*2))
		}
		operation, cancel := context.WithDeadline(ctx, deadline)
		err := context.Cause(operation)
		if err == nil {
			err = held.renew(operation)
		}
		if err == nil && !time.Now().Before(deadline) {
			err = context.DeadlineExceeded
		}
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			last, next = start, time.Until(start.Add(interval))
			continue
		}
		if l.ttl == 0 || errors.Is(err, errNotOwned) || !time.Now().Before(last.Add(l.ttl/3*2)) {
			return err
		}
		next = min(l.options.RetryInterval, time.Until(last.Add(l.ttl/3*2)))
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
