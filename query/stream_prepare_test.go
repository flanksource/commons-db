package query_test

import (
	stdcontext "context"
	"errors"
	"fmt"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
)

// preparedLog is the order a session acquired, read, and released its data in,
// and fails the failAt-th preparation.
type preparedLog struct {
	mu       sync.Mutex
	entries  []string
	prepared int
	failAt   int
}

func (l *preparedLog) add(entry string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, entry)
}

func (l *preparedLog) prepare(_ stdcontext.Context, p query.Profile, supplied map[string]any) (func(), error) {
	l.mu.Lock()
	l.prepared++
	attempt := l.prepared
	l.entries = append(l.entries, fmt.Sprintf("prepare %s stream=%v", p.Name, supplied["stream"]))
	l.mu.Unlock()
	release := func() { l.add(fmt.Sprintf("release %d", attempt)) }
	if attempt == l.failAt {
		return release, errors.New("index unavailable")
	}
	return release, nil
}

func (l *preparedLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.entries...)
}

type preparedProvider struct {
	typ string
	log *preparedLog
	err error
}

func (p preparedProvider) Type() string { return p.typ }

func (p preparedProvider) Execute(context.Context, query.ProviderRequest) ([]query.Row, error) {
	p.log.add("sample")
	return []query.Row{{"n": 1.0}}, p.err
}

var _ = Describe("ExecuteStream with a registry that prepares reads", func() {
	// start opens a top session over a fresh provider, whose registry fails the
	// failAt-th preparation.
	start := func(name string, failAt int) (*preparedLog, *query.Session, error) {
		log := &preparedLog{failAt: failAt}
		query.RegisterProvider(preparedProvider{typ: name, log: log})
		registry := query.NewSessionRegistry(query.RegistryOptions{BeforeRead: log.prepare})
		profile := query.Profile{
			Name: name, Provider: query.ProviderConfig{Type: name},
			Params: []query.ParamDef{{Name: "stream"}},
			Top:    &query.TopSpec{Interval: types.Duration{Duration: time.Second}},
		}
		session, err := query.ExecuteStream(context.New(), registry, profile, map[string]any{"stream": "run-1"})
		if session != nil {
			DeferCleanup(session.Stop, "spec cleanup")
		}
		return log, session, err
	}

	It("prepares a top session's data as it starts and again before every later sample", func() {
		log, _, err := start("prepared-top", 0)
		Expect(err).ToNot(HaveOccurred())
		Eventually(log.snapshot, "5s", "20ms").Should(HaveLen(6))
		Expect(log.snapshot()[:6]).To(Equal([]string{
			"prepare prepared-top stream=run-1", "sample", "release 1",
			"prepare prepared-top stream=run-1", "sample", "release 2",
		}))
	})

	It("refuses to start a session whose data cannot be prepared, and reads nothing", func() {
		log, _, err := start("prepared-refused", 1)
		Expect(errors.Is(err, query.ErrPrepareRead)).To(BeTrue(), "%v", err)
		Expect(err).To(MatchError(ContainSubstring("index unavailable")))
		Expect(log.snapshot()).To(Equal([]string{"prepare prepared-refused stream=run-1", "release 1"}))
	})

	It("fails a top session whose data cannot be prepared for a later sample", func() {
		log, session, err := start("prepared-later", 2)
		Expect(err).ToNot(HaveOccurred())
		waitState(session, query.SessionFailed)
		Expect(session.Snapshot().Error).To(ContainSubstring("index unavailable"))
		Expect(log.snapshot()).To(Equal([]string{
			"prepare prepared-later stream=run-1", "sample", "release 1",
			"prepare prepared-later stream=run-1", "release 2",
		}))
	})

	It("releases the initial top preparation when its sample fails", func() {
		log := &preparedLog{}
		query.RegisterProvider(preparedProvider{typ: "prepared-sample-error", log: log, err: errors.New("query unavailable")})
		registry := query.NewSessionRegistry(query.RegistryOptions{BeforeRead: log.prepare})
		profile := query.Profile{
			Name: "prepared-sample-error", Provider: query.ProviderConfig{Type: "prepared-sample-error"},
			Params: []query.ParamDef{{Name: "stream"}}, Top: &query.TopSpec{Interval: types.Duration{Duration: time.Second}},
		}

		session, err := query.ExecuteStream(context.New(), registry, profile, map[string]any{"stream": "run-1"})
		Expect(err).ToNot(HaveOccurred())
		waitState(session, query.SessionFailed)
		Expect(log.snapshot()).To(Equal([]string{
			"prepare prepared-sample-error stream=run-1", "sample", "release 1",
		}))
	})

	It("holds a trace's preparation until its stream exits", func() {
		log := &preparedLog{}
		query.RegisterProvider(preparedStreamProvider{typ: "prepared-trace", log: log})
		registry := query.NewSessionRegistry(query.RegistryOptions{BeforeRead: log.prepare})
		profile := query.Profile{
			Name: "prepared-trace", Provider: query.ProviderConfig{Type: "prepared-trace"},
			Params: []query.ParamDef{{Name: "stream"}}, Trace: &query.TraceSpec{},
		}

		session, err := query.ExecuteStream(context.New(), registry, profile, map[string]any{"stream": "run-1"})
		Expect(err).ToNot(HaveOccurred())
		waitState(session, query.SessionCompleted)
		Eventually(log.snapshot, "5s", "20ms").Should(Equal([]string{
			"prepare prepared-trace stream=run-1", "stream", "release 1",
		}))
	})

	It("releases a trace preparation when setup refuses the provider", func() {
		log := &preparedLog{}
		query.RegisterProvider(preparedProvider{typ: "prepared-not-streaming", log: log})
		registry := query.NewSessionRegistry(query.RegistryOptions{BeforeRead: log.prepare})
		profile := query.Profile{
			Name: "prepared-not-streaming", Provider: query.ProviderConfig{Type: "prepared-not-streaming"},
			Params: []query.ParamDef{{Name: "stream"}}, Trace: &query.TraceSpec{},
		}

		_, err := query.ExecuteStream(context.New(), registry, profile, map[string]any{"stream": "run-1"})
		Expect(err).To(MatchError(ContainSubstring("does not support streaming")))
		Expect(log.snapshot()).To(Equal([]string{
			"prepare prepared-not-streaming stream=run-1", "release 1",
		}))
	})

	It("releases a trace preparation when the registry refuses to add the session", func() {
		log := &preparedLog{}
		query.RegisterProvider(preparedStreamProvider{typ: "prepared-capacity", log: log})
		registry := query.NewSessionRegistry(query.RegistryOptions{MaxSessions: 1, BeforeRead: log.prepare})
		active, err := query.NewSession(query.SessionOptions{
			ID: "active", Profile: query.Profile{Name: "active", Trace: &query.TraceSpec{}},
			Kind: query.KindTrace, Role: query.SessionRoleCapture, MaxEvents: 1,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(registry.Add(active)).To(Succeed())
		DeferCleanup(active.Stop, "spec cleanup")
		profile := query.Profile{
			Name: "prepared-capacity", Provider: query.ProviderConfig{Type: "prepared-capacity"},
			Params: []query.ParamDef{{Name: "stream"}}, Trace: &query.TraceSpec{},
		}

		_, err = query.ExecuteStream(context.New(), registry, profile, map[string]any{"stream": "run-1"})
		Expect(errors.Is(err, query.ErrMaxSessions)).To(BeTrue(), "%v", err)
		Expect(log.snapshot()).To(Equal([]string{
			"prepare prepared-capacity stream=run-1", "release 1",
		}))
	})

	It("holds a trace preparation until a stopped stream exits", func() {
		log := &preparedLog{}
		query.RegisterProvider(preparedStreamProvider{typ: "prepared-stop", log: log, block: true})
		registry := query.NewSessionRegistry(query.RegistryOptions{BeforeRead: log.prepare})
		profile := query.Profile{
			Name: "prepared-stop", Provider: query.ProviderConfig{Type: "prepared-stop"},
			Params: []query.ParamDef{{Name: "stream"}}, Trace: &query.TraceSpec{},
		}

		session, err := query.ExecuteStream(context.New(), registry, profile, map[string]any{"stream": "run-1"})
		Expect(err).ToNot(HaveOccurred())
		Eventually(log.snapshot, "5s", "20ms").Should(Equal([]string{
			"prepare prepared-stop stream=run-1", "stream",
		}))
		session.Stop("stopped by spec")
		waitState(session, query.SessionStopped)
		Eventually(log.snapshot, "5s", "20ms").Should(Equal([]string{
			"prepare prepared-stop stream=run-1", "stream", "stream stopped", "release 1",
		}))
	})

	It("keeps a top session's resolved parameters isolated from its sampler", func() {
		provider := &parameterMutatingProvider{typ: "isolated-top", sampled: make(chan struct{})}
		query.RegisterProvider(provider)
		profile := query.Profile{
			Name: "isolated-top", Provider: query.ProviderConfig{Type: provider.typ},
			Params: []query.ParamDef{{Name: "stream"}}, Top: &query.TopSpec{Interval: types.Duration{Duration: time.Second}},
		}

		session, err := query.ExecuteStream(context.New(), query.NewSessionRegistry(query.RegistryOptions{}), profile, map[string]any{"stream": "run-1"})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(session.Stop, "spec cleanup")
		Eventually(provider.sampled, "5s").Should(BeClosed())
		Expect(session.Snapshot().Params).To(HaveKeyWithValue("stream", "run-1"))
	})
})

type parameterMutatingProvider struct {
	typ     string
	sampled chan struct{}
	once    sync.Once
}

func (p *parameterMutatingProvider) Type() string { return p.typ }

func (p *parameterMutatingProvider) Execute(_ context.Context, request query.ProviderRequest) ([]query.Row, error) {
	request.Params["stream"] = "sampler-mutated"
	p.once.Do(func() { close(p.sampled) })
	return []query.Row{{"n": 1.0}}, nil
}

type preparedStreamProvider struct {
	typ   string
	log   *preparedLog
	block bool
}

func (p preparedStreamProvider) Type() string { return p.typ }

func (p preparedStreamProvider) Execute(context.Context, query.ProviderRequest) ([]query.Row, error) {
	return nil, nil
}

func (p preparedStreamProvider) Stream(ctx context.Context, _ query.ProviderRequest, _ func(query.Row)) error {
	p.log.add("stream")
	if p.block {
		<-ctx.Done()
		p.log.add("stream stopped")
		return ctx.Err()
	}
	return nil
}
