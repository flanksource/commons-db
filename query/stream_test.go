package query_test

import (
	stdcontext "context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fakeStreamProvider emits scripted rows then either returns err, ends, or
// blocks until the context is cancelled.
type fakeStreamProvider struct {
	typ   string
	rows  []query.Row
	err   error
	block bool

	// emitted, when set, is closed once every row has been handed to the
	// session's runner, which has then received it.
	emitted chan struct{}
}

func (f *fakeStreamProvider) Type() string { return f.typ }

func (f *fakeStreamProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	return f.rows, f.err
}

func (f *fakeStreamProvider) Stream(ctx context.Context, req query.ProviderRequest, emit func(query.Row)) error {
	for _, row := range f.rows {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		emit(row)
	}
	if f.emitted != nil {
		close(f.emitted)
	}
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.err
}

func traceProfile(providerType string) query.Profile {
	return query.Profile{
		Name:     "trace-" + providerType,
		Provider: query.ProviderConfig{Type: providerType},
		Trace:    &query.TraceSpec{},
	}
}

// backendTimeout is a provider's own store call running out of time while the
// session that made it is still live.
var backendTimeout = fmt.Errorf("wait on stream %q: %w", "jvm-probe:cycle", stdcontext.DeadlineExceeded)

func waitState(s *query.Session, state query.SessionState) {
	GinkgoHelper()
	Eventually(func() query.SessionState { return s.Snapshot().State }, "5s", "10ms").Should(Equal(state))
}

var _ = Describe("ExecuteStream trace", func() {
	newRegistry := func() *query.SessionRegistry {
		return query.NewSessionRegistry(query.RegistryOptions{})
	}

	It("streams rows through per-event CEL columns until the source ends", func() {
		query.RegisterProvider(&fakeStreamProvider{
			typ:  "stream-happy",
			rows: []query.Row{{"duration_ms": 1500.0}, {"duration_ms": 500.0}},
		})
		p := traceProfile("stream-happy")
		p.Columns = []query.ColumnDef{{Name: "duration_s", CEL: "row.duration_ms / 1000.0"}}

		s, err := query.ExecuteStream(context.New(), newRegistry(), p)
		Expect(err).ToNot(HaveOccurred())
		waitState(s, query.SessionCompleted)

		events := s.Events()
		Expect(events).To(HaveLen(2))
		Expect(events[0].Row).To(HaveKeyWithValue("duration_s", 1.5))
		Expect(events[0].Sequence).To(Equal(int64(1)))
	})

	It("runs page-capable processors before per-event column mapping", func() {
		query.RegisterProcessor(rawFirstProcessor{})
		query.RegisterProvider(&fakeStreamProvider{typ: "stream-processor-order", rows: []query.Row{{"raw": "alpha"}}})
		profile := rawFirstProfile("stream-processor-order", "stream-processor-order", "test.raw-first")
		profile.Trace = &query.TraceSpec{}

		session, err := query.ExecuteStream(context.New(), newRegistry(), profile)
		Expect(err).ToNot(HaveOccurred())
		waitState(session, query.SessionCompleted)

		Expect(session.Events()).To(HaveLen(1))
		Expect(session.Events()[0].Row).To(HaveKeyWithValue("mapped", "alpha-processed-aliased-mapped"))
	})

	It("requires a buffer for a whole-result trace processor", func() {
		query.RegisterProcessor(wholeRawFirstProcessor{})
		query.RegisterProvider(&fakeStreamProvider{typ: "stream-unbuffered-whole"})
		profile := traceProfile("stream-unbuffered-whole")
		profile.Processors = []query.ProcessorSpec{{Type: "test.whole-raw-first"}}

		_, err := query.ExecuteStream(context.New(), newRegistry(), profile)

		Expect(err).To(MatchError(ContainSubstring("trace.buffer")))
		Expect(err).To(MatchError(ContainSubstring("test.whole-raw-first")))
	})

	It("flushes whole-result processors at the configured raw row count", func() {
		query.RegisterProcessor(wholeRawFirstProcessor{})
		query.RegisterProvider(&fakeStreamProvider{
			typ:  "stream-count-buffer",
			rows: []query.Row{{"raw": "alpha"}, {"raw": "beta"}, {"raw": "gamma"}},
		})
		profile := rawFirstProfile("stream-count-buffer", "stream-count-buffer", "test.whole-raw-first")
		profile.Trace = &query.TraceSpec{Buffer: &query.TraceBufferSpec{MaxRows: 2}}

		session, err := query.ExecuteStream(context.New(), newRegistry(), profile)
		Expect(err).ToNot(HaveOccurred())
		waitState(session, query.SessionCompleted)

		events := session.Events()
		Expect(events).To(HaveLen(3))
		Expect(events[0].Row).To(HaveKeyWithValue("batchSize", int64(2)))
		Expect(events[1].Row).To(HaveKeyWithValue("batchSize", int64(2)))
		Expect(events[2].Row).To(HaveKeyWithValue("batchSize", int64(1)))
		Expect(events[2].Row).To(HaveKeyWithValue("mapped", "gamma-processed-aliased-mapped"))
	})

	It("flushes a partial buffer when maxWait elapses", func() {
		query.RegisterProcessor(wholeRawFirstProcessor{})
		query.RegisterProvider(&fakeStreamProvider{
			typ: "stream-time-buffer", rows: []query.Row{{"raw": "alpha"}}, block: true,
		})
		profile := rawFirstProfile("stream-time-buffer", "stream-time-buffer", "test.whole-raw-first")
		profile.Trace = &query.TraceSpec{Buffer: &query.TraceBufferSpec{
			MaxRows: 100,
			MaxWait: types.Duration{Duration: 25 * time.Millisecond},
		}}

		session, err := query.ExecuteStream(context.New(), newRegistry(), profile)
		Expect(err).ToNot(HaveOccurred())
		Eventually(session.Events, "5s", "10ms").Should(HaveLen(1))
		Expect(session.Snapshot().State).To(Equal(query.SessionRunning))
		session.Stop("stopped by spec")
		waitState(session, query.SessionStopped)
	})

	It("flushes a partial buffer before an explicit stop becomes terminal", func() {
		query.RegisterProcessor(wholeRawFirstProcessor{})
		emitted := make(chan struct{})
		query.RegisterProvider(&fakeStreamProvider{
			typ: "stream-stop-buffer", rows: []query.Row{{"raw": "alpha"}}, block: true, emitted: emitted,
		})
		profile := rawFirstProfile("stream-stop-buffer", "stream-stop-buffer", "test.whole-raw-first")
		profile.Trace = &query.TraceSpec{Buffer: &query.TraceBufferSpec{MaxRows: 100}}

		session, err := query.ExecuteStream(context.New(), newRegistry(), profile)
		Expect(err).ToNot(HaveOccurred())
		Eventually(emitted, "5s").Should(BeClosed(), "the row is buffered, not yet emitted, before the stop")
		Expect(session.Events()).To(BeEmpty())
		session.Stop("stopped by spec")
		waitState(session, query.SessionStopped)

		Expect(session.Events()).To(HaveLen(1))
		Expect(session.Events()[0].Row).To(HaveKeyWithValue("mapped", "alpha-processed-aliased-mapped"))
	})

	It("flushes a partial buffer when the session deadline is reached", func() {
		query.RegisterProcessor(wholeRawFirstProcessor{})
		query.RegisterProvider(&fakeStreamProvider{
			typ: "stream-deadline-buffer", rows: []query.Row{{"raw": "alpha"}}, block: true,
		})
		profile := rawFirstProfile("stream-deadline-buffer", "stream-deadline-buffer", "test.whole-raw-first")
		profile.Trace = &query.TraceSpec{
			MaxDuration: types.Duration{Duration: 30 * time.Millisecond},
			Buffer:      &query.TraceBufferSpec{MaxRows: 100},
		}

		session, err := query.ExecuteStream(context.New(), newRegistry(), profile)
		Expect(err).ToNot(HaveOccurred())
		waitState(session, query.SessionCompleted)

		Expect(session.Events()).To(HaveLen(1))
		Expect(session.Events()[0].Row).To(HaveKeyWithValue("mapped", "alpha-processed-aliased-mapped"))
	})

	DescribeTable("fails the session when the provider errors",
		func(providerType string, failure error) {
			query.RegisterProvider(&fakeStreamProvider{typ: providerType, err: failure})

			s, err := query.ExecuteStream(context.New(), newRegistry(), traceProfile(providerType))
			Expect(err).ToNot(HaveOccurred())
			waitState(s, query.SessionFailed)
			Expect(s.Snapshot().Error).To(ContainSubstring(failure.Error()))
		},
		Entry("with its own error", "stream-err", errors.New("socket closed")),
		// A follow's store read carries its own deadline; that one expiring is
		// the store failing, not the session's bound ending the stream.
		Entry("with a timeout of its own backend call while the session is live", "stream-backend-timeout", backendTimeout),
	)

	It("tears down a blocked provider on Stop", func() {
		query.RegisterProvider(&fakeStreamProvider{typ: "stream-block", rows: []query.Row{{"n": 1}}, block: true})

		s, err := query.ExecuteStream(context.New(), newRegistry(), traceProfile("stream-block"))
		Expect(err).ToNot(HaveOccurred())
		waitState(s, query.SessionRunning)

		s.Stop("stopped by spec")
		waitState(s, query.SessionStopped)
	})

	It("completes when MaxDuration elapses", func() {
		query.RegisterProvider(&fakeStreamProvider{typ: "stream-timeout", block: true})
		p := traceProfile("stream-timeout")
		p.Trace.MaxDuration = types.Duration{Duration: 50 * time.Millisecond}

		s, err := query.ExecuteStream(context.New(), newRegistry(), p)
		Expect(err).ToNot(HaveOccurred())
		waitState(s, query.SessionCompleted)
	})

	It("rejects providers without streaming support", func() {
		query.RegisterProvider(&mockProvider{typ: "stream-unsupported"})

		_, err := query.ExecuteStream(context.New(), newRegistry(), traceProfile("stream-unsupported"))
		Expect(err).To(MatchError(ContainSubstring("does not support streaming")))
	})

	It("rejects plain query profiles", func() {
		_, err := query.ExecuteStream(context.New(), newRegistry(), query.Profile{Name: "plain"})
		Expect(err).To(MatchError(ContainSubstring("neither trace nor top")))
	})

	It("materializes the buffered events as a Result", func() {
		query.RegisterProvider(&fakeStreamProvider{typ: "stream-result", rows: []query.Row{{"n": 1}, {"n": 2}}})

		s, err := query.ExecuteStream(context.New(), newRegistry(), traceProfile("stream-result"))
		Expect(err).ToNot(HaveOccurred())
		waitState(s, query.SessionCompleted)

		result, err := s.Result(context.New())
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(Equal([]query.Row{{"n": 1}, {"n": 2}}))
	})
})

// filterCapturingStream records the native filters its request carried, which
// is the only place a resolved time selection is observable.
type filterCapturingStream struct {
	typ     string
	filters chan []query.ColumnFilterValue
}

func (f *filterCapturingStream) Type() string { return f.typ }

func (f *filterCapturingStream) Execute(context.Context, query.ProviderRequest) ([]query.Row, error) {
	return nil, nil
}

func (f *filterCapturingStream) Stream(_ context.Context, req query.ProviderRequest, _ func(query.Row)) error {
	f.filters <- req.Filters
	return nil
}

var _ = Describe("Follow", func() {
	newRegistry := func() *query.SessionRegistry {
		return query.NewSessionRegistry(query.RegistryOptions{})
	}

	windowedProfile := func(providerType string) query.Profile {
		return query.Profile{
			Name:     "window-" + providerType,
			Provider: query.ProviderConfig{Type: providerType},
			Params: []query.ParamDef{
				{Name: "Start", Type: query.ParamTypeDateTime, Role: query.ParamRoleTimeFrom, Default: "now-1h"},
				{Name: "End", Type: query.ParamTypeDateTime, Role: query.ParamRoleTimeTo, Default: "now"},
			},
		}
	}

	It("resolves a lower bound but no upper one, so the tail never expires", func() {
		query.RegisterProvider(&fakeStreamProvider{typ: "follow-window"})

		session, err := query.ExecuteStream(context.New(), newRegistry(), query.Follow(windowedProfile("follow-window")))
		Expect(err).ToNot(HaveOccurred())
		waitState(session, query.SessionCompleted)

		params := session.Snapshot().Params
		Expect(params).To(HaveKey("Start"))
		Expect(params).ToNot(HaveKey("End"), "an end instant is a deadline the tail would stop at")
	})

	// windowedTraceFilters runs p with a bounded time selection and returns the
	// filters the provider was actually sent.
	//
	// Registered under a provider type that accepts native column filters: the
	// binding gate is what decides whether a time control exists at all, and no
	// real backend is linked into this suite.
	windowedTraceFilters := func(name string, trace *query.TraceSpec) []query.ColumnFilterValue {
		captured := make(chan []query.ColumnFilterValue, 1)
		query.RegisterProvider(&filterCapturingStream{typ: "opensearch", filters: captured})

		session, err := query.ExecuteStream(context.New(), newRegistry(), query.Profile{
			Name:     name,
			Provider: query.ProviderConfig{Type: "opensearch"},
			Trace:    trace,
			Columns: []query.ColumnDef{{
				Name: "timestamp", Type: query.ColumnTypeDateTime,
				Filter: &query.ColumnFilterDef{Field: "@timestamp"},
			}},
		}, map[string]any{"filter.timestamp": ">=now-1h,<=now"})
		Expect(err).ToNot(HaveOccurred())
		waitState(session, query.SessionCompleted)
		return <-captured
	}

	It("keeps the start of a followed profile's time selection and drops its end", func() {
		followed := query.Follow(query.Profile{Name: "tail-window"})

		filters := windowedTraceFilters("tail-window", followed.Trace)

		Expect(filters).To(HaveLen(1))
		Expect(filters[0].Range.Min).ToNot(BeNil(), "the tail still starts somewhere")
		Expect(filters[0].Range.Max).To(BeNil(), "an end instant is where the tail would stop")
	})

	It("leaves a declared trace's window alone", func() {
		// The inverse of the case above, and the reason TraceSpec.Follow exists. A
		// profile that writes down a window is stating what the session is; a
		// request that asks to follow one is stating something about this run. Only
		// the second may reach in and delete half of the first.
		filters := windowedTraceFilters("declared-window", &query.TraceSpec{})

		Expect(filters).To(HaveLen(1))
		Expect(filters[0].Range.Max).ToNot(BeNil(), "an author's closing bound is theirs to keep")
	})

	It("buffers raw rows so a whole-result processor still sees a batch", func() {
		followed := query.Follow(query.Profile{Name: "unbuffered"})
		Expect(followed.Trace.Buffer).To(Equal(&query.TraceBufferSpec{
			MaxRows: 200, MaxWait: types.Duration{Duration: time.Second},
		}))
	})

	It("keeps a buffer the profile declared for itself", func() {
		declared := query.Profile{
			Name:  "declared",
			Trace: &query.TraceSpec{Buffer: &query.TraceBufferSpec{MaxRows: 7}},
		}
		Expect(query.Follow(declared).Trace.Buffer.MaxRows).To(Equal(7))
	})

	It("leaves the profile it was handed untouched", func() {
		stored := windowedProfile("follow-copy")
		stored.Trace = &query.TraceSpec{}

		_ = query.Follow(stored)

		Expect(stored.Trace.Buffer).To(BeNil())
		Expect(stored.Params).To(HaveLen(2))
	})
})

// countingProvider returns a scripted result per call, failing with failure
// from failAt.
type countingProvider struct {
	typ     string
	calls   atomic.Int64
	failAt  int64
	failure error
}

func (c *countingProvider) Type() string { return c.typ }

func (c *countingProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	n := c.calls.Add(1)
	if c.failAt > 0 && n >= c.failAt {
		return nil, c.failure
	}
	return []query.Row{{"tick": float64(n), "n": 1.0}, {"tick": float64(n), "n": 3.0}, {"tick": float64(n), "n": 2.0}}, nil
}

var _ = Describe("ExecuteStream top", func() {
	newRegistry := func() *query.SessionRegistry {
		return query.NewSessionRegistry(query.RegistryOptions{})
	}

	topProfile := func(providerType string) query.Profile {
		return query.Profile{
			Name:     "top-" + providerType,
			Provider: query.ProviderConfig{Type: providerType},
			Top: &query.TopSpec{
				Interval: types.Duration{Duration: time.Second},
				SortBy:   "n",
				Limit:    2,
			},
		}
	}

	It("samples on the interval, replacing the latest snapshot", func() {
		provider := &countingProvider{typ: "top-ticks"}
		query.RegisterProvider(provider)

		s, err := query.ExecuteStream(context.New(), newRegistry(), topProfile("top-ticks"))
		Expect(err).ToNot(HaveOccurred())
		Eventually(func() int64 { return provider.calls.Load() }, "5s", "20ms").Should(BeNumerically(">=", 2))
		defer s.Stop("stopped by spec")

		Eventually(func() any {
			latest := s.Latest()
			if latest == nil || len(latest.Rows) == 0 {
				return nil
			}
			return latest.Rows[0]["tick"]
		}, "5s", "20ms").Should(BeNumerically(">=", 2), "latest snapshot is replaced, not appended")

		latest := s.Latest()
		Expect(latest.Rows).To(HaveLen(2), "limit applied")
		Expect(latest.Rows[0]["n"]).To(Equal(3.0), "sorted descending by n")
	})

	DescribeTable("fails the session when a tick errors",
		func(providerType string, failure error) {
			query.RegisterProvider(&countingProvider{typ: providerType, failAt: 1, failure: failure})

			s, err := query.ExecuteStream(context.New(), newRegistry(), topProfile(providerType))
			Expect(err).ToNot(HaveOccurred())
			waitState(s, query.SessionFailed)
			Expect(s.Snapshot().Error).To(ContainSubstring(failure.Error()))

			events := s.Events()
			Expect(events[len(events)-1].Error).To(ContainSubstring(failure.Error()))
		},
		Entry("with its own error", "top-fail", errors.New("backend gone")),
		Entry("with a timeout of its own backend call while the session is live", "top-backend-timeout", backendTimeout),
	)

	It("executes a single tick synchronously via Execute", func() {
		query.RegisterProvider(&countingProvider{typ: "top-sync"})

		result, err := query.Execute(context.New(), topProfile("top-sync"))
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(2))
		Expect(result.Rows[0]["n"]).To(Equal(3.0))
	})

	It("refuses to Execute a trace profile synchronously", func() {
		_, err := query.Execute(context.New(), traceProfile("any"))
		Expect(err).To(MatchError(ContainSubstring("use ExecuteStream")))
	})
})
