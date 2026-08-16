package query

import (
	stdcontext "context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/flanksource/commons-db/context"
)

// StreamProvider is an optional provider capability for continuous sources
// (log tails, event streams). Trace profiles require it.
type StreamProvider interface {
	Provider

	// Stream runs req, calling emit for each row until ctx is cancelled or the
	// source ends. It blocks; a nil return means the source ended normally.
	Stream(ctx context.Context, req ProviderRequest, emit func(Row)) error
}

// ExecuteStream starts a trace or top session and returns immediately with the
// session in the starting state. ctx must be a long-lived application context;
// the run is bounded only by the session's clamped MaxDuration or Stop().
func ExecuteStream(ctx context.Context, reg *SessionRegistry, p Profile, params ...map[string]any) (*Session, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.Kind() == KindQuery {
		return nil, fmt.Errorf("profile %q declares neither trace nor top; use Execute", p.Name)
	}
	if p.Namespace != "" {
		ctx = ctx.WithNamespace(p.Namespace)
	}
	var supplied map[string]any
	if len(params) > 0 {
		supplied = params[0]
	}
	resolved, filters, err := resolveProfileInput(p, supplied)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}

	if p.Kind() == KindTrace {
		return startTrace(ctx, reg, p, resolved, filters)
	}
	return startTop(ctx, reg, p, resolved, filters)
}

func startTrace(ctx context.Context, reg *SessionRegistry, p Profile, resolved map[string]any, filters []ColumnFilterValue) (*Session, error) {
	provider, err := GetProvider(p.Provider.Type)
	if err != nil {
		return nil, err
	}
	sp, ok := provider.(StreamProvider)
	if !ok {
		return nil, fmt.Errorf("profile %q: provider %q does not support streaming", p.Name, p.Provider.Type)
	}
	req, err := buildProviderRequest(ctx, p.Provider, p.Query, p.Params, resolved)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	req.Filters = filters
	if p.Trace.Buffer == nil {
		label, err := nonPageProcessor(p.Processors)
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", p.Name, err)
		}
		if label != "" {
			return nil, fmt.Errorf(
				"profile %q: processor %q needs the whole result; configure trace.buffer.maxRows or trace.buffer.maxWait",
				p.Name, label)
		}
	}
	pipeline, err := newTracePipeline(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}

	session := newRegisteredSession(reg, p, resolved, reg.ClampEvents(p.Trace.EventLimit()))
	if err := reg.Add(session); err != nil {
		return nil, err
	}
	runCtx, cancel := ctx.WithTimeout(reg.ClampDuration(p.Trace.DurationLimit()))
	session.setCancel(cancel)

	go runTrace(runCtx, cancel, sp, session, req, pipeline, p.Trace.Buffer)
	return session, nil
}

func runTrace(
	ctx context.Context,
	cancel stdcontext.CancelFunc,
	sp StreamProvider,
	session *Session,
	req ProviderRequest,
	pipeline *tracePipeline,
	buffer *TraceBufferSpec,
) {
	defer cancel()
	session.markRunning()
	rows, done := streamTraceRows(ctx, sp, req)
	runner := traceRunner{
		ctx: ctx, session: session, pipeline: pipeline, buffer: buffer,
		rows: rows, done: done,
	}
	session.markDone(normalizeStreamErr(runner.run()))
}

type tracePipeline struct {
	ctx      context.Context
	profile  Profile
	page     *pageProcessorChain
	buffered bool
}

func newTracePipeline(ctx context.Context, profile Profile) (*tracePipeline, error) {
	pipeline := &tracePipeline{ctx: ctx, profile: profile, buffered: profile.Trace.Buffer != nil}
	if pipeline.buffered || len(profile.Processors) == 0 {
		return pipeline, nil
	}
	page, err := newPageProcessorChain(profile.Processors, nil)
	if err != nil {
		return nil, err
	}
	pipeline.page = page
	return pipeline, nil
}

func (p *tracePipeline) Process(rows []Row) ([]Row, error) {
	result := &Result{Profile: p.profile.Name, Rows: rows}
	var err error
	if p.buffered {
		result, err = applyProcessors(p.ctx, p.profile.Processors, result)
	} else if p.page != nil {
		var page Page
		page, err = p.page.Process(p.ctx, Page{Rows: rows})
		result.Rows = page.Rows
	}
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.profile.Name, err)
	}
	mapped, _, err := applyRowTransforms(p.ctx, p.profile, result.Rows)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.profile.Name, err)
	}
	return mapped, nil
}

func streamTraceRows(ctx context.Context, provider StreamProvider, req ProviderRequest) (<-chan Row, <-chan error) {
	rows := make(chan Row)
	done := make(chan error, 1)
	go func() {
		done <- provider.Stream(ctx, req, func(row Row) {
			select {
			case rows <- row:
			case <-ctx.Done():
			}
		})
	}()
	return rows, done
}

type traceRunner struct {
	ctx      context.Context
	session  *Session
	pipeline *tracePipeline
	buffer   *TraceBufferSpec
	rows     <-chan Row
	done     <-chan error
	pending  []Row
	timer    *time.Timer
	timerC   <-chan time.Time
}

func (r *traceRunner) run() error {
	defer r.stopTimer()
	for {
		select {
		case row := <-r.rows:
			if err := r.add(row); err != nil {
				return err
			}
		case err := <-r.done:
			return r.finish(err)
		case <-r.ctx.Done():
			return r.finish(r.ctx.Err())
		case <-r.timerC:
			if err := r.flush(); err != nil {
				return err
			}
		}
	}
}

func (r *traceRunner) add(row Row) error {
	if r.buffer == nil {
		return r.emit([]Row{row})
	}
	r.pending = append(r.pending, row)
	if len(r.pending) == 1 && r.buffer.MaxWait.Duration > 0 {
		r.timer = time.NewTimer(r.buffer.MaxWait.Duration)
		r.timerC = r.timer.C
	}
	if r.buffer.MaxRows > 0 && len(r.pending) >= r.buffer.MaxRows {
		return r.flush()
	}
	return nil
}

func (r *traceRunner) finish(streamErr error) error {
	if err := r.flush(); err != nil {
		return err
	}
	return streamErr
}

func (r *traceRunner) flush() error {
	if len(r.pending) == 0 {
		return nil
	}
	r.stopTimer()
	rows := r.pending
	r.pending = nil
	return r.emit(rows)
}

func (r *traceRunner) emit(raw []Row) error {
	rows, err := r.pipeline.Process(raw)
	if err != nil {
		return err
	}
	for _, row := range rows {
		r.session.Emit(Event{Row: row})
	}
	return nil
}

func (r *traceRunner) stopTimer() {
	if r.timer != nil {
		r.timer.Stop()
	}
	r.timer = nil
	r.timerC = nil
}

func startTop(ctx context.Context, reg *SessionRegistry, p Profile, resolved map[string]any, filters []ColumnFilterValue) (*Session, error) {
	if _, err := GetProvider(p.Provider.Type); err != nil {
		return nil, err
	}
	session := newRegisteredSession(reg, p, resolved, reg.ClampEvents(0))
	if err := reg.Add(session); err != nil {
		return nil, err
	}
	runCtx, cancel := ctx.WithTimeout(reg.ClampDuration(p.Top.DurationLimit()))
	session.setCancel(cancel)

	go runTop(runCtx, cancel, session, p, resolved, filters)
	return session, nil
}

func runTop(ctx context.Context, cancel stdcontext.CancelFunc, s *Session, p Profile, resolved map[string]any, filters []ColumnFilterValue) {
	defer cancel()
	s.markRunning()

	ticker := time.NewTicker(p.Top.TickInterval())
	defer ticker.Stop()
	for {
		result, err := executeResolved(ctx, p, resolved, filters)
		if err != nil {
			if norm := normalizeStreamErr(err); norm != nil {
				s.Emit(Event{Error: norm.Error()})
				s.markDone(norm)
			} else {
				s.markDone(nil)
			}
			return
		}
		s.setLatest(result)
		s.Emit(Event{Rows: result.Rows})

		select {
		case <-ctx.Done():
			s.markDone(nil)
			return
		case <-ticker.C:
		}
	}
}

func newRegisteredSession(reg *SessionRegistry, p Profile, resolved map[string]any, maxEvents int) *Session {
	return NewSession(SessionOptions{
		ID:           uuid.NewString(),
		Profile:      p,
		Params:       resolved,
		MaxEvents:    maxEvents,
		OnEvent:      reg.opts.OnEvent,
		OnTransition: reg.opts.OnTransition,
	})
}

// normalizeStreamErr treats cancellation and the session's own deadline as a
// normal end of stream, not a failure.
func normalizeStreamErr(err error) error {
	if err == nil || errors.Is(err, stdcontext.Canceled) || errors.Is(err, stdcontext.DeadlineExceeded) {
		return nil
	}
	return err
}
