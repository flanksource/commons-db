package query_test

import (
	"errors"
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

	It("fails the session when the provider errors", func() {
		query.RegisterProvider(&fakeStreamProvider{typ: "stream-err", err: errors.New("socket closed")})

		s, err := query.ExecuteStream(context.New(), newRegistry(), traceProfile("stream-err"))
		Expect(err).ToNot(HaveOccurred())
		waitState(s, query.SessionFailed)
		Expect(s.Snapshot().Error).To(ContainSubstring("socket closed"))
	})

	It("tears down a blocked provider on Stop", func() {
		query.RegisterProvider(&fakeStreamProvider{typ: "stream-block", rows: []query.Row{{"n": 1}}, block: true})

		s, err := query.ExecuteStream(context.New(), newRegistry(), traceProfile("stream-block"))
		Expect(err).ToNot(HaveOccurred())
		waitState(s, query.SessionRunning)

		s.Stop()
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

// countingProvider returns a scripted result per call, erroring at failAt.
type countingProvider struct {
	typ    string
	calls  atomic.Int64
	failAt int64
}

func (c *countingProvider) Type() string { return c.typ }

func (c *countingProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	n := c.calls.Add(1)
	if c.failAt > 0 && n >= c.failAt {
		return nil, errors.New("backend gone")
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
		defer s.Stop()

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

	It("fails the session when a tick errors", func() {
		query.RegisterProvider(&countingProvider{typ: "top-fail", failAt: 1})

		s, err := query.ExecuteStream(context.New(), newRegistry(), topProfile("top-fail"))
		Expect(err).ToNot(HaveOccurred())
		waitState(s, query.SessionFailed)
		Expect(s.Snapshot().Error).To(ContainSubstring("backend gone"))

		events := s.Events()
		Expect(events[len(events)-1].Error).To(ContainSubstring("backend gone"))
	})

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
