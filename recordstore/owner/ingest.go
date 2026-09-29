// The owner's ingestion: every published batch applied once, in each
// producer's order, then trashed; one it cannot read is moved to failed/.
package owner

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons/logger"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/spool"
)

const (
	// maxBatchRows is the most rows the owner applies from one batch; a
	// larger one goes to failed/ rather than holding the writer that long.
	maxBatchRows = 1_000_000

	// outOfOrderWait is how long a producer's batch waits for the one before
	// it before it is applied anyway.
	outOfOrderWait = time.Minute

	// retryAttempts and retryFor bound how long a batch the store keeps
	// failing to apply is retried before it goes to failed/.
	retryAttempts = 20
	retryFor      = 10 * time.Minute

	minRetry = 100 * time.Millisecond
	maxRetry = 30 * time.Second

	// requestWait bounds how long a control request waits for batches, inside
	// the socket's deadline.
	requestWait = 9 * time.Second
)

type retry struct {
	attempts int
	first    time.Time
}

type ingester[T Target] struct {
	dir        spool.Dir
	backend    T
	configured string
	poke       chan struct{}

	// draining lets one drain run at a time: the loop's, or Close's.
	draining sync.Mutex

	mu        sync.Mutex
	applied   chan struct{}
	waiting   map[string]time.Time
	retries   map[string]retry
	backlog   int
	failed    int
	lastError string
}

func newIngester[T Target](dir spool.Dir, backend T, configured string) *ingester[T] {
	return &ingester[T]{
		dir: dir, backend: backend, configured: configured, poke: make(chan struct{}, 1),
		applied: make(chan struct{}), waiting: map[string]time.Time{}, retries: map[string]retry{},
	}
}

// loop drains the spool until ctx ends: at once when nudged, otherwise
// polling from minPoll up to poll while it is idle, and backing off from
// minRetry to maxRetry while the store fails.
func (i *ingester[T]) loop(ctx context.Context, poll time.Duration) {
	interval, backoff := minPoll, minRetry
	for {
		applied, err := i.drain(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			logger.Errorf("record store owner %s: ingest: %v", i.configured, err)
			interval, backoff = backoff, min(backoff*2, maxRetry)
		case applied > 0:
			interval, backoff = minPoll, minRetry
		default:
			interval, backoff = min(interval*2, poll), minRetry
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-i.poke:
			timer.Stop()
			interval = minPoll
		case <-timer.C:
		}
	}
}

// drain applies every published batch it can, oldest first. A producer's
// batch whose predecessor has not been applied waits for it, up to
// outOfOrderWait. It stops at the first batch the store fails to apply, so
// no later batch overtakes it.
func (i *ingester[T]) drain(ctx context.Context) (int, error) {
	i.draining.Lock()
	defer i.draining.Unlock()
	names, err := i.dir.Incoming()
	if err != nil {
		return 0, err
	}
	i.mu.Lock()
	i.backlog = len(names)
	i.mu.Unlock()
	applied := 0
	defer func() {
		if applied > 0 {
			i.mu.Lock()
			close(i.applied)
			i.applied = make(chan struct{})
			i.mu.Unlock()
		}
	}()
	last := map[string]int64{}
	for _, name := range names {
		if ctx.Err() != nil {
			return applied, ctx.Err()
		}
		batch, err := i.dir.Load(name)
		if err != nil {
			i.fail(name, err)
			continue
		}
		if rows := batchRows(batch); rows > maxBatchRows {
			i.fail(name, fmt.Errorf("batch %s holds %d rows, more than the %d the owner applies at once", batch.ID, rows, maxBatchRows))
			continue
		}
		instance := batch.Producer.Instance
		seq, known := last[instance]
		if !known {
			if seq, err = i.backend.ProducerSeq(ctx, instance); err != nil {
				return applied, err
			}
			last[instance] = seq
		}
		if seq > 0 && batch.Producer.Seq > seq+1 && !i.waitedOut(name, batch) {
			continue
		}
		if _, err := i.backend.AppendBatch(ctx, batch); err != nil {
			if ctx.Err() == nil {
				i.retry(name, err)
			}
			return applied, err
		}
		failpoint("after-ledger-commit")
		last[instance] = max(seq, batch.Producer.Seq)
		i.applied1(name)
		applied++
	}
	return applied, nil
}

func batchRows(batch recordstore.Batch) int {
	rows := 0
	for _, entry := range batch.Entries {
		rows += len(entry.Rows)
	}
	return rows
}

// waitedOut reports whether name, a batch whose predecessor is missing, has
// waited for it long enough to be applied anyway.
func (i *ingester[T]) waitedOut(name string, batch recordstore.Batch) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	first, seen := i.waiting[name]
	if !seen {
		i.waiting[name] = time.Now()
		return false
	}
	if time.Since(first) < outOfOrderWait {
		return false
	}
	logger.Errorf("record store owner %s: applying batch %s of producer %s (seq %d) without the batch before it, which never arrived",
		i.configured, batch.ID, batch.Producer.Instance, batch.Producer.Seq)
	return true
}

// applied1 moves an applied batch to trash.
func (i *ingester[T]) applied1(name string) {
	i.mu.Lock()
	delete(i.waiting, name)
	delete(i.retries, name)
	i.mu.Unlock()
	failpoint("before-trash")
	if err := i.dir.Trash(name); err != nil {
		// The ledger holds the batch; ingesting it again answers from there.
		logger.Errorf("record store owner %s: trash %s: %v", i.configured, name, err)
	}
}

// retry counts a failure to apply name, and fails the batch once it has been
// retried too often or too long.
func (i *ingester[T]) retry(name string, err error) {
	i.mu.Lock()
	state, seen := i.retries[name]
	if !seen {
		state.first = time.Now()
	}
	state.attempts++
	i.retries[name] = state
	i.lastError = err.Error()
	exhausted := state.attempts >= retryAttempts || time.Since(state.first) >= retryFor
	i.mu.Unlock()
	if exhausted {
		i.fail(name, fmt.Errorf("gave up after %d attempts: %w", state.attempts, err))
	}
}

// fail moves name to failed/, where its producer finds why.
func (i *ingester[T]) fail(name string, err error) {
	logger.Errorf("record store owner %s: batch %s failed: %v", i.configured, name, err)
	i.mu.Lock()
	delete(i.waiting, name)
	delete(i.retries, name)
	i.failed++
	i.lastError = err.Error()
	i.mu.Unlock()
	if moveErr := i.dir.Fail(name, err); moveErr != nil {
		logger.Errorf("record store owner %s: move %s to failed: %v", i.configured, name, moveErr)
	}
}

// request is the control socket's ingest: it nudges the loop and, with wait,
// answers once the batches ids name are applied, or at requestWait with the
// ones that are.
func (i *ingester[T]) request(ids []string, wait bool) ([]string, error) {
	select {
	case i.poke <- struct{}{}:
	default:
	}
	if !wait {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestWait)
	defer cancel()
	for {
		i.mu.Lock()
		applied := i.applied
		i.mu.Unlock()
		var found []string
		for _, id := range ids {
			if _, ok, err := i.backend.BatchOutcome(ctx, id); err == nil && ok {
				found = append(found, id)
			}
		}
		if len(found) == len(ids) {
			return found, nil
		}
		select {
		case <-ctx.Done():
			return found, nil
		case <-applied:
		}
	}
}

// figures are the spool's backlog, the batches failed and the last error.
func (i *ingester[T]) figures() (int, int, string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.backlog, i.failed, i.lastError
}

// collect removes the spool's trash and old failures, and the ledger rows
// older than ledgerKeep whose batches are no longer in incoming.
func (i *ingester[T]) collect(ctx context.Context, now time.Time) error {
	if err := i.dir.Collect(now, spool.CollectOptions{}); err != nil {
		return err
	}
	names, err := i.dir.Incoming()
	if err != nil {
		return err
	}
	_, err = i.backend.SweepBatches(ctx, now.Add(-ledgerKeep), func(id string) bool {
		for _, name := range names {
			if strings.HasSuffix(name, "-"+id) {
				return true
			}
		}
		return false
	})
	return err
}
