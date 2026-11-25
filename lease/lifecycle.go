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
	})
}

func (l *managedLease) Unlock(context.Context) error {
	l.end(nil)
	l.stop()
	<-l.cleaned
	return l.unlockErr
}

func (l *Backend) manage(ctx context.Context, held Ownership, start time.Time) *managedLease {
	run, cancel := context.WithCancel(context.WithoutCancel(ctx))
	lease := &managedLease{done: make(chan struct{}), cleaned: make(chan struct{}), stop: cancel}
	go func() {
		defer close(lease.cleaned)
		if err := l.maintain(run, held, start); err != nil {
			lease.end(fmt.Errorf("%w: %w", ErrLost, err))
		}
		// Unlock acknowledges that protected work has stopped.
		<-run.Done()
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(run), l.Options.OperationTimeout)
		defer cancel()
		if err := held.Unlock(cleanup); err != nil && !errors.Is(err, ErrNotOwned) {
			lease.unlockErr = err
		}
	}()
	return lease
}

func (l *Backend) maintain(ctx context.Context, held Ownership, last time.Time) error {
	interval := 10 * time.Second
	if l.TTL != 0 {
		interval = l.TTL / 3
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
		if l.TTL != 0 {
			deadline = minTime(deadline, last.Add(l.TTL/3*2))
		}
		operation, cancel := context.WithDeadline(ctx, deadline)
		err := context.Cause(operation)
		if err == nil {
			err = held.Renew(operation)
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
		if l.TTL == 0 || errors.Is(err, ErrNotOwned) || !time.Now().Before(last.Add(l.TTL/3*2)) {
			return err
		}
		next = min(l.Options.RetryInterval, time.Until(last.Add(l.TTL/3*2)))
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
