package sqltrace

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/commons-db/tracing/xetrace"
)

// ActiveTrace is one live or finished XE capture. Its ID is the record stream
// its rows are committed to.
type ActiveTrace struct {
	ID          string
	SessionName string
	// Statements created and started the XE session, as the factory ran them.
	Statements []string
	// Database is a readable label for the database patterns the capture's
	// session is predicated on (the context's database when none were asked for).
	Database  string
	StartedAt time.Time
	StopAt    time.Time
	StoppedAt time.Time
	Options   xetrace.CreateOptions
	Error     string

	mu       sync.Mutex
	xe       XESession
	writer   *eventWriter
	appender recordAppender
	running  bool
	stopOnce sync.Once
	cancel   context.CancelFunc
	// done is closed by runDrain once the final drain is committed, the stream
	// sealed and the session dropped. stop() waits on it so a synchronous Stop
	// observes the events captured right before cancellation — critical for
	// spans shorter than the poll interval, where no background poll ever fired.
	done       chan struct{}
	finalDelay time.Duration
}

// Running reports whether the drain goroutine is still polling.
func (t *ActiveTrace) Running() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

// Err returns the terminal capture, store or cleanup failure, if one occurred.
func (t *ActiveTrace) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Error == "" {
		return nil
	}
	return errors.New(t.Error)
}

// Result is the capture's metadata and whole-capture summary. Its rows are in
// the record stream; a caller attaches the ref and preview it reports.
func (t *ActiveTrace) Result() xetrace.TraceResult {
	summary := t.Summary()
	t.mu.Lock()
	defer t.mu.Unlock()
	stopped := t.StoppedAt
	if stopped.IsZero() {
		stopped = time.Now().UTC()
	}
	return xetrace.TraceResult{
		SessionName: t.SessionName, Database: t.Database, StartedAt: t.StartedAt, StoppedAt: stopped,
		Duration: stopped.Sub(t.StartedAt), Summary: &summary, Error: t.Error,
	}
}

// failCapture stops a capture whose rows can no longer be recorded. The drain
// then finishes as it would on Stop, and the store failure becomes its error.
func (t *ActiveTrace) failCapture(error) {
	t.stopOnce.Do(func() { t.cancel() })
}

func (t *ActiveTrace) finish(stoppedAt time.Time, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.running = false
	if t.StoppedAt.IsZero() {
		t.StoppedAt = stoppedAt
	}
	if err != nil {
		t.Error = err.Error()
	}
}

// stop signals the drain loop to cancel and waits (bounded) for runDrain to
// finish its final drain, commit, seal and drop. Safe to call multiple times.
func (t *ActiveTrace) stop() error {
	t.stopOnce.Do(func() { t.cancel() })
	select {
	case <-t.done:
	case <-time.After(stopDrainTimeout(t.finalDelay)):
		return fmt.Errorf("timed out waiting for drain of session %q", t.SessionName)
	}
	return t.Err()
}
