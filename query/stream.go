package query

import (
	stdcontext "context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/types"
)

// StreamProvider is an optional provider capability for continuous sources
// (log tails, event streams). Trace profiles require it.
type StreamProvider interface {
	Provider

	// Stream runs req, calling emit for each row until ctx is cancelled or the
	// source ends. It blocks; a nil return means the source ended normally.
	Stream(ctx context.Context, req ProviderRequest, emit func(Row)) error
}

// followBufferRows and followBufferWait bound the raw-row batches a followed
// profile hands its processors. tracePipeline without a buffer processes one row
// at a time, so a page-capable processor — logs.dedupe is the one every log
// profile reaches for — is handed a single row and folds nothing. The same bound
// decides how many SSE frames a busy tail produces, which is the other reason
// not to leave it at one row per frame.
const (
	followBufferRows = 200
	followBufferWait = time.Second
)

// Follow rewrites p as a session that tails its source from here onward: the
// promotion a plain query profile gets when a caller asks to follow it rather
// than run it once. The provider must implement StreamProvider — SupportsStreaming
// answers that, and the transport is expected to have asked before promoting, so
// a surface never offers a Follow control it cannot honour.
//
// Dropping the time-to parameter is the whole of "from here onward". It reads as
// the opposite of what a cursor walk does, and it is: a walk pins the instant its
// date math resolved against (see the clock stamped into the cursor by
// ExecutePages) because every page after the first must name the same result set,
// and a rolling window that moved between pages would stale the token. A follow
// has no result set to name — it is waiting for rows that do not exist yet — so
// an upper bound resolved at start is simply the moment it stops tailing. Only
// that edge goes; the lower bound is where "here" begins, and a follow that
// replayed from the beginning of retention every time would be a different
// feature.
//
// The returned profile shares nothing mutable with p: the caller's profile came
// out of a store other requests read too.
func Follow(p Profile) Profile {
	params := make([]ParamDef, 0, len(p.Params))
	for _, param := range p.Params {
		if param.Role == ParamRoleTimeTo {
			continue
		}
		params = append(params, param)
	}
	p.Params = params

	spec := TraceSpec{}
	if p.Trace != nil {
		spec = *p.Trace
	}
	if spec.Buffer == nil {
		spec.Buffer = &TraceBufferSpec{
			MaxRows: followBufferRows,
			MaxWait: types.Duration{Duration: followBufferWait},
		}
	}
	spec.Follow = true
	p.Trace = &spec
	return p
}

// ExecuteStream starts a trace or top session and returns immediately with the
// session in the starting state. ctx must be a long-lived application context;
// the run is bounded only by the session's clamped MaxDuration or Stop().
//
// A followed trace (p.Trace.Follow) is a view over data that already exists: it
// is never persisted. Every other session is a capture, begun and updated
// through the registry's SessionStore when one is configured.
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
	resolved, filters, err := resolveProfileInput(p, supplied, time.Now())
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	release, err := reg.prepareRead(ctx, p, supplied)
	if err != nil {
		return nil, err
	}

	if p.Kind() == KindTrace {
		return startTrace(ctx, reg, p, resolved, filters, release)
	}
	return startTop(ctx, reg, topSampler{
		profile: p, resolved: resolved, filters: filters, supplied: supplied, release: release,
	})
}

func startTrace(ctx context.Context, reg *SessionRegistry, p Profile, resolved map[string]any, filters []ColumnFilterValue, release func()) (*Session, error) {
	started := false
	defer func() {
		if !started {
			release()
		}
	}()

	provider, err := GetProvider(p.Provider.Type)
	if err != nil {
		return nil, err
	}
	sp, ok := provider.(StreamProvider)
	if !ok {
		return nil, fmt.Errorf("profile %q: provider %q does not support streaming", p.Name, p.Provider.Type)
	}
	req, err := buildProviderRequest(ctx, provider, p.Provider, p.Query, p.Params, resolved)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	req.Filters = filters
	if p.Trace.Follow {
		req.Filters = openTailWindow(filters)
	}
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

	run, err := reg.startStream(ctx, p, resolved, reg.ClampEvents(p.Trace.EventLimit()), p.Trace.DurationLimit())
	if err != nil {
		return nil, err
	}

	go runTrace(run, sp, req, pipeline, p.Trace.Buffer, release)
	started = true
	return run.session, nil
}

// openTailWindow drops the closing edge of a followed trace's time selections.
//
// A follow reads from now onward, so an end instant resolved when it started is
// not a bound on what it reads but the moment it would stop reading. It is the
// exact inverse of what a cursor walk needs, where the window is pinned so that
// every page names one result set; the two rules look contradictory and are,
// because a walk reads a result that already exists and a tail waits for one
// that does not.
//
// It applies only to a session promoted by ?follow=true, never to a trace an
// author declared — see TraceSpec.Follow. A profile that writes down a window is
// making a statement about what the session is, and quietly deleting half of it
// would be a worse answer than honouring a bound the author can see and change.
//
// Only selections are opened here. The other closing edge a profile can carry is
// a time-to parameter, which is also interpolated into the query text, so
// removing it rewrites the profile rather than one request — see Follow.
func openTailWindow(filters []ColumnFilterValue) []ColumnFilterValue {
	open := make([]ColumnFilterValue, 0, len(filters))
	for _, filter := range filters {
		bounded := filter.Kind == ColumnFilterKindTime || filter.Kind == ColumnFilterKindDate
		if !bounded || filter.Range == nil || filter.Range.Max == nil {
			open = append(open, filter)
			continue
		}
		if filter.Range.Min == nil {
			// The selection was only an end; with it gone nothing is selected, and
			// an empty range would compile to a clause matching everything anyway.
			continue
		}
		filter.Range = &FilterRange{Min: filter.Range.Min}
		open = append(open, filter)
	}
	return open
}

func runTrace(
	run streamRun,
	sp StreamProvider,
	req ProviderRequest,
	pipeline *tracePipeline,
	buffer *TraceBufferSpec,
	release func(),
) {
	defer release()
	defer run.cancel()
	ctx, session := run.ctx, run.session
	markStreamRunning(session)
	deliveryCtx, stopDelivery := stdcontext.WithCancel(stdcontext.Background())
	defer stopDelivery()
	rows, done := streamTraceRows(ctx, sp, req, deliveryCtx)
	runner := traceRunner{
		session: session, pipeline: pipeline, buffer: buffer,
		rows: rows, done: done,
	}
	err := normalizeStreamErr(ctx, runner.run())
	run.cancel()
	stopDelivery()
	if !runner.providerFinished {
		// A processor failure must also wait for provider cleanup. Stop accepting
		// rows first so a provider's final drain cannot block on the failed reader.
		err = errors.Join(err, normalizeStreamErr(ctx, <-done))
	}
	session.Finish(FinishUpdate{Err: err})
}

// markStreamRunning moves a stream session to running. A session already
// stopped (a stop racing the start) has nothing to run, which its run context
// being cancelled already tells the runner.
func markStreamRunning(session *Session) {
	if err := session.Running(RunningUpdate{}); err != nil && !errors.Is(err, ErrSessionEnded) {
		panic(fmt.Sprintf("query: mark stream session running: %v", err))
	}
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

func streamTraceRows(ctx context.Context, provider StreamProvider, req ProviderRequest, deliveryCtx stdcontext.Context) (<-chan Row, <-chan error) {
	rows := make(chan Row)
	done := make(chan error, 1)
	go func() {
		done <- provider.Stream(ctx, req, func(row Row) {
			select {
			case rows <- row:
			case <-deliveryCtx.Done():
			}
		})
	}()
	return rows, done
}

type traceRunner struct {
	session          *Session
	pipeline         *tracePipeline
	buffer           *TraceBufferSpec
	rows             <-chan Row
	done             <-chan error
	pending          []Row
	timer            *time.Timer
	timerC           <-chan time.Time
	providerFinished bool
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
			r.providerFinished = true
			return r.finish(err)
		// Cancellation asks the provider to stop; its return confirms that final
		// rows and server-side teardown have finished before the session ends.
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
	// Presented as one batch, as a page is: a profile that declares no columns
	// derives them from the rows rendered together.
	presented, err := PresentClickyRows(r.pipeline.profile, rows)
	if err != nil {
		return fmt.Errorf("profile %q: present rows: %w", r.pipeline.profile.Name, err)
	}
	for index, row := range rows {
		r.session.Emit(Event{Row: row, ClickyRow: &presented[index]})
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

// topSampler is one top session's query: the profile, its resolved input, and
// the input as the caller supplied it, which every later sample is prepared
// with.
type topSampler struct {
	registry *SessionRegistry
	session  *Session
	profile  Profile
	resolved map[string]any
	filters  []ColumnFilterValue
	supplied map[string]any
	release  func()
}

func startTop(ctx context.Context, reg *SessionRegistry, sampler topSampler) (*Session, error) {
	started := false
	defer func() {
		if !started {
			sampler.release()
		}
	}()

	p := sampler.profile
	if _, err := GetProvider(p.Provider.Type); err != nil {
		return nil, err
	}
	run, err := reg.startStream(ctx, p, maps.Clone(sampler.resolved), reg.ClampEvents(0), p.Top.DurationLimit())
	if err != nil {
		return nil, err
	}

	sampler.registry, sampler.session = reg, run.session
	go sampler.run(run.ctx, run.cancel)
	started = true
	return run.session, nil
}

// run samples until the session ends. The first sample reads data
// ExecuteStream already prepared; every later one prepares it again first.
func (t topSampler) run(ctx context.Context, cancel stdcontext.CancelFunc) {
	defer cancel()
	markStreamRunning(t.session)

	ticker := time.NewTicker(t.profile.Top.TickInterval())
	defer ticker.Stop()
	for first := true; ; first = false {
		result, err := t.sample(ctx, first)
		if err != nil {
			norm := normalizeStreamErr(ctx, err)
			if norm != nil {
				t.session.Emit(Event{Error: norm.Error()})
			}
			t.session.Finish(FinishUpdate{Err: norm})
			return
		}
		t.session.setLatest(result)
		t.session.Emit(Event{Rows: result.Rows})

		select {
		case <-ctx.Done():
			t.session.Finish(FinishUpdate{})
			return
		case <-ticker.C:
		}
	}
}

func (t topSampler) sample(ctx context.Context, prepared bool) (*Result, error) {
	release := t.release
	if !prepared {
		var err error
		release, err = t.registry.prepareRead(ctx, t.profile, t.supplied)
		if err != nil {
			return nil, err
		}
	}
	defer release()
	return executeResolved(ctx, t.profile, t.resolved, t.filters)
}

// normalizeStreamErr treats cancellation and the session's own deadline as a
// normal end of stream, not a failure. Only ctx — the session's run context —
// ending makes a context error normal: a provider's store call that carries its
// own deadline wraps DeadlineExceeded too, and that one timing out while the
// session is live is the store failing.
func normalizeStreamErr(ctx stdcontext.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil && (errors.Is(err, stdcontext.Canceled) || errors.Is(err, stdcontext.DeadlineExceeded)) {
		return nil
	}
	return err
}
