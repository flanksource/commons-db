package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type versionMonitor struct {
	mu        sync.Mutex
	intervals map[*time.Ticker]time.Duration
	wake      chan struct{}
	ready     chan struct{}
	readyOnce sync.Once
	done      chan struct{}
	cancel    context.CancelFunc
	version   atomic.Int64
	err       error
}

// WatchDataVersion observes commits on a shared, connection-affine monitor.
// Its dedicated read-only connection never reserves a query pool slot.
func (d *DB) WatchDataVersion(ctx context.Context, every time.Duration, changed func()) error {
	if every <= 0 || changed == nil {
		return fmt.Errorf("sqlite: watch %s: a positive interval and change callback are required", d.path)
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	monitor, err := d.subscribeVersion(ticker, every)
	if errors.Is(err, ErrClosed) {
		return nil
	}
	if err != nil {
		return err
	}
	defer d.unsubscribeVersion(monitor, ticker)
	select {
	case <-ctx.Done():
		return nil
	case <-d.stop:
		return nil
	case <-monitor.ready:
	}
	last := monitor.version.Load()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-d.stop:
			return nil
		case <-monitor.done:
			return monitor.err
		case <-ticker.C:
			if version := monitor.version.Load(); version != last {
				last = version
				changed()
			}
		}
	}
}

func (d *DB) subscribeVersion(ticker *time.Ticker, every time.Duration) (*versionMonitor, error) {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if d.closing {
		return nil, ErrClosed
	}
	d.watching.Add(1)
	if d.monitor == nil {
		ctx, cancel := context.WithCancel(context.Background())
		monitor := &versionMonitor{intervals: map[*time.Ticker]time.Duration{}, wake: make(chan struct{}, 1), ready: make(chan struct{}), done: make(chan struct{}), cancel: cancel}
		d.monitor = monitor
		go func() {
			monitor.err = monitor.run(ctx, d.path)
			close(monitor.done)
			monitor.readyOnce.Do(func() { close(monitor.ready) })
		}()
	}
	monitor := d.monitor
	monitor.mu.Lock()
	monitor.intervals[ticker] = every
	monitor.mu.Unlock()
	select {
	case monitor.wake <- struct{}{}:
	default:
	}
	return monitor, nil
}

func (d *DB) unsubscribeVersion(monitor *versionMonitor, ticker *time.Ticker) {
	defer d.watching.Done()
	d.stateMu.Lock()
	monitor.mu.Lock()
	delete(monitor.intervals, ticker)
	last := len(monitor.intervals) == 0
	monitor.mu.Unlock()
	if last {
		d.monitor = nil
		monitor.cancel()
	}
	d.stateMu.Unlock()
	if last {
		<-monitor.done
	}
}

func (m *versionMonitor) interval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	var interval time.Duration
	for _, every := range m.intervals {
		if interval == 0 || every < interval {
			interval = every
		}
	}
	return interval
}

func (m *versionMonitor) run(ctx context.Context, path string) (err error) {
	observer, err := sql.Open("sqlite", ReadOnlyDSN(path))
	if err != nil {
		return fmt.Errorf("sqlite: monitor %s: %w", path, err)
	}
	observer.SetMaxOpenConns(1)
	defer func() { err = errors.Join(err, observer.Close()) }()
	conn, err := observer.Conn(ctx)
	if err != nil {
		return monitorError(ctx, path, err)
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	if err := m.read(ctx, conn); err != nil {
		return monitorError(ctx, path, err)
	}
	m.readyOnce.Do(func() { close(m.ready) })
	next := time.Now().Add(m.interval())
	timer := time.NewTimer(time.Until(next))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-m.wake:
			if earlier := time.Now().Add(m.interval()); earlier.Before(next) {
				next = earlier
				timer.Reset(time.Until(next))
			}
		case <-timer.C:
			if err := m.read(ctx, conn); err != nil {
				return monitorError(ctx, path, err)
			}
			next = time.Now().Add(m.interval())
			timer.Reset(time.Until(next))
		}
	}
}

func (m *versionMonitor) read(ctx context.Context, conn *sql.Conn) error {
	var version int64
	if err := conn.QueryRowContext(ctx, "PRAGMA data_version").Scan(&version); err != nil {
		return err
	}
	m.version.Store(version)
	return nil
}

func monitorError(ctx context.Context, path string, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	return fmt.Errorf("sqlite: monitor %s: %w", path, err)
}
