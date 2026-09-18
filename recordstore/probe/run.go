package probe

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// Status is the durable restart checkpoint a managed session records in its
// summary. A successor may pass Cursor back through Options.
type Status struct {
	Cursor       Cursor `json:"cursor"`
	SourceActive bool   `json:"sourceActive"`
	Appended     int64  `json:"appended"`
	Skipped      int64  `json:"skipped"`
	Source       any    `json:"source,omitempty"`
}

// Window describes what one Sample appended and the cursor it committed.
type Window struct {
	Cursor  Cursor             `json:"cursor"`
	Stored  recordstore.Window `json:"stored"`
	Rows    int64              `json:"rows"`
	Skipped int64              `json:"skipped"`
	Active  bool               `json:"active"`
	Source  any                `json:"source,omitempty"`
	Events  *query.EventsRef   `json:"events,omitempty"`
}

// Run implements query.ManagedRun over one Source.
type Run struct {
	manager *Manager
	options Options
	source  Source
	base    context.Context

	callMu      sync.Mutex
	mu          sync.Mutex
	status      Status
	events      *query.EventsRef
	ended       bool
	finish      query.ManagedFinish
	endErr      error
	finalWindow Window

	finished     chan struct{}
	finishedOnce sync.Once
	leaseCancel  context.CancelFunc
	leaseDone    chan struct{}
	leaseErr     error
}

func newRun(manager *Manager, options Options, source Source, ref *query.EventsRef, base context.Context) *Run {
	run := &Run{
		manager: manager, options: options, source: source, base: base,
		status: Status{Cursor: options.Cursor, SourceActive: true}, events: ref,
		finished: make(chan struct{}),
	}
	if options.RenewEvery > 0 {
		leaseCtx, cancel := context.WithCancel(base)
		run.leaseCancel, run.leaseDone = cancel, make(chan struct{})
		go run.renewLease(leaseCtx)
	}
	return run
}

// Status returns the state query.SessionRegistry mirrors into its record.
func (r *Run) Status() query.ManagedStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := r.status
	return query.ManagedStatus{
		Handle: r.options.Identity, Events: cloneEventsRef(r.events),
		EventCount: eventTotal(r.events), Summary: status,
	}
}

// ProbeStatus returns the source checkpoint without the session projection.
func (r *Run) ProbeStatus() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// FinalWindow is what Stop drained and finalized. It is empty before Stop.
func (r *Run) FinalWindow() Window {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finalWindow
}

func (r *Run) Finished() <-chan struct{} { return r.finished }

// Sample reads pages until the source cursor catches its write cursor.
func (r *Run) Sample(ctx context.Context) (any, error) {
	r.callMu.Lock()
	defer r.callMu.Unlock()
	if r.ended {
		return nil, fmt.Errorf("probe %q: %w", r.options.Identity, query.ErrSessionEnded)
	}
	return r.readPages(ctx)
}

func (r *Run) readPages(ctx context.Context) (Window, error) {
	window := Window{Cursor: r.cursor(), Active: true}
	for page := 0; page < r.options.MaxPages; page++ {
		cursor := r.cursor()
		batch, err := r.source.Read(ctx, cursor)
		if err != nil {
			return window, err
		}
		next, err := validateBatch(r.options.Identity, cursor, batch)
		if err != nil {
			return window, err
		}
		appended := recordstore.AppendResult{Window: recordstore.Window{From: 1}}
		if len(batch.Rows) > 0 {
			appended, err = r.options.Backend.Append(ctx, r.options.Stream, r.options.Kind, batch.Rows)
			if err != nil {
				return window, err
			}
		}
		if committer, ok := r.source.(Committer); ok {
			if err := committer.Commit(ctx, batch); err != nil {
				return window, fmt.Errorf("probe %q: commit cursor %d: %w", r.options.Identity, next.Next, err)
			}
		}
		r.commit(next, batch, appended)
		accumulateWindow(&window, appended)
		window.Cursor, window.Active, window.Source = next, batch.Active, batch.Summary
		if err := r.refresh(ctx); err != nil {
			return window, err
		}
		window.Events = r.eventRef()
		if !batch.Active {
			r.signalFinished()
		}
		if !batch.More {
			return window, nil
		}
	}
	return window, fmt.Errorf("probe %q: source remains behind after %d pages", r.options.Identity, r.options.MaxPages)
}

func validateBatch(identity string, cursor Cursor, batch Batch) (Cursor, error) {
	switch {
	case batch.Generation == "":
		return Cursor{}, fmt.Errorf("probe %q: source returned an empty generation", identity)
	case cursor.Generation != "" && batch.Generation != cursor.Generation:
		return Cursor{}, fmt.Errorf("probe %q: source generation %q does not match cursor generation %q", identity, batch.Generation, cursor.Generation)
	case batch.Next < cursor.Next:
		return Cursor{}, fmt.Errorf("probe %q: source cursor moved backward from %d to %d", identity, cursor.Next, batch.Next)
	case batch.Write < batch.Next:
		return Cursor{}, fmt.Errorf("probe %q: source write cursor %d is behind next cursor %d", identity, batch.Write, batch.Next)
	case batch.More && batch.Next == cursor.Next:
		return Cursor{}, fmt.Errorf("probe %q: source cursor %d did not advance while write cursor is %d", identity, batch.Next, batch.Write)
	}
	return Cursor{Generation: batch.Generation, Next: batch.Next}, nil
}

func (r *Run) commit(cursor Cursor, batch Batch, appended recordstore.AppendResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.Cursor = cursor
	r.status.SourceActive = batch.Active
	r.status.Appended += appended.Window.Len()
	r.status.Skipped += appended.Skipped
	r.status.Source = batch.Summary
}

func accumulateWindow(window *Window, appended recordstore.AppendResult) {
	stored := appended.Window.Len()
	if stored > 0 {
		if window.Rows == 0 {
			window.Stored.From = appended.Window.From
		}
		window.Stored.To = appended.Window.To
		window.Rows += stored
	}
	window.Skipped += appended.Skipped
}

func (r *Run) cursor() Cursor {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status.Cursor
}

func (r *Run) refresh(ctx context.Context) error {
	ref, err := r.options.Describe(ctx, r.options.Stream)
	if err != nil {
		return fmt.Errorf("probe %q: describe stream %q: %w", r.options.Identity, r.options.Stream, err)
	}
	if ref == nil {
		return fmt.Errorf("probe %q: describe stream %q returned no reference", r.options.Identity, r.options.Stream)
	}
	r.mu.Lock()
	r.events = ref
	r.mu.Unlock()
	return nil
}

func (r *Run) eventRef() *query.EventsRef {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneEventsRef(r.events)
}

func cloneEventsRef(ref *query.EventsRef) *query.EventsRef {
	if ref == nil {
		return nil
	}
	copy := *ref
	return &copy
}

func eventTotal(ref *query.EventsRef) int64 {
	if ref == nil {
		return 0
	}
	return ref.Total
}

func (r *Run) signalFinished() { r.finishedOnce.Do(func() { close(r.finished) }) }

func (r *Run) renewLease(ctx context.Context) {
	defer close(r.leaseDone)
	ticker := time.NewTicker(r.options.RenewEvery)
	defer ticker.Stop()
	renewer := r.source.(Renewer)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := renewer.Renew(ctx); err != nil {
				r.mu.Lock()
				r.leaseErr = fmt.Errorf("probe %q: renew lease: %w", r.options.Identity, err)
				r.mu.Unlock()
				r.signalFinished()
				return
			}
		}
	}
}

func (r *Run) stopLease() error {
	if r.leaseCancel != nil {
		r.leaseCancel()
		<-r.leaseDone
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.leaseErr
}

// Stop freezes the source, drains it, materializes final rows, seals the
// stream, and only then releases the source claim.
func (r *Run) Stop(ctx context.Context) (query.ManagedFinish, error) {
	r.callMu.Lock()
	defer r.callMu.Unlock()
	if r.ended {
		return r.finish, r.endErr
	}
	r.ended = true
	defer r.complete()

	var final Final
	window := Window{Cursor: r.cursor(), Active: true}
	r.endErr = r.stopLease()
	if err := r.source.Freeze(ctx); err != nil {
		r.endErr = errors.Join(r.endErr, fmt.Errorf("probe %q: freeze: %w", r.options.Identity, err))
	} else if window, err = r.readPages(ctx); err != nil {
		r.endErr = errors.Join(r.endErr, fmt.Errorf("probe %q: drain: %w", r.options.Identity, err))
	} else if final, err = r.source.Finalize(ctx); err != nil {
		r.endErr = errors.Join(r.endErr, fmt.Errorf("probe %q: finalize: %w", r.options.Identity, err))
	} else if appended, appendErr := r.appendFinal(ctx, final.Rows); appendErr != nil {
		r.endErr = errors.Join(r.endErr, appendErr)
	} else if err = r.commitFinal(ctx, final, appended, &window); err != nil {
		r.endErr = errors.Join(r.endErr, err)
	} else if err = r.options.Backend.Seal(ctx, r.options.Stream); err != nil {
		r.endErr = errors.Join(r.endErr, fmt.Errorf("probe %q: seal stream: %w", r.options.Identity, err))
	} else if err = r.refresh(ctx); err != nil {
		r.endErr = errors.Join(r.endErr, err)
	}
	if err := r.source.Release(ctx); err != nil {
		r.endErr = errors.Join(r.endErr, fmt.Errorf("probe %q: release: %w", r.options.Identity, err))
	}
	r.mu.Lock()
	r.status.SourceActive = false
	if final.Summary != nil {
		r.status.Source = final.Summary
	}
	window.Active = false
	window.Events = cloneEventsRef(r.events)
	r.finalWindow = window
	r.mu.Unlock()
	r.finish = r.managedFinish(final)
	return r.finish, r.endErr
}

func (r *Run) appendFinal(ctx context.Context, rows []recordstore.Row) (recordstore.AppendResult, error) {
	if len(rows) == 0 {
		return recordstore.AppendResult{Window: recordstore.Window{From: 1}}, nil
	}
	appended, err := r.options.Backend.Append(ctx, r.options.Stream, r.options.Kind, rows)
	if err != nil {
		return recordstore.AppendResult{}, fmt.Errorf("probe %q: append final rows: %w", r.options.Identity, err)
	}
	return appended, nil
}

func (r *Run) commitFinal(ctx context.Context, final Final, appended recordstore.AppendResult, window *Window) error {
	if committer, ok := r.source.(FinalCommitter); ok {
		if err := committer.CommitFinal(ctx, final); err != nil {
			return fmt.Errorf("probe %q: commit final rows: %w", r.options.Identity, err)
		}
	}
	accumulateWindow(window, appended)
	r.mu.Lock()
	r.status.Appended += appended.Window.Len()
	r.status.Skipped += appended.Skipped
	if final.Summary != nil {
		r.status.Source = final.Summary
	}
	r.mu.Unlock()
	return nil
}

// Detach ends local ownership without touching the source or its stream.
func (r *Run) Detach(ctx context.Context) (query.ManagedFinish, error) {
	r.callMu.Lock()
	defer r.callMu.Unlock()
	if r.ended {
		return r.finish, r.endErr
	}
	r.ended = true
	r.endErr = r.stopLease()
	if detacher, ok := r.source.(Detacher); ok {
		r.endErr = errors.Join(r.endErr, detacher.Detach(ctx))
	}
	r.finish = r.managedFinish(Final{})
	r.complete()
	return r.finish, r.endErr
}

func (r *Run) managedFinish(final Final) query.ManagedFinish {
	status := r.Status()
	return query.ManagedFinish{
		ManagedStatus: status, Warning: final.Warning, Result: final.Result,
	}
}

func (r *Run) complete() {
	r.signalFinished()
	r.manager.forget(r.options.Identity, r)
}
