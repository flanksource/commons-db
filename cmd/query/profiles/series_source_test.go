package profiles

import (
	"context"
	"fmt"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	clickymetrics "github.com/flanksource/clicky/metrics"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/profilestore"
)

// seriesProvider answers every query with two pods' samples at the start and
// end of the range it was given, recording each request it served.
type seriesProvider struct {
	mu       *sync.Mutex
	requests *[]query.ProviderRequest
	envs     *[]any
	// gate, when set, holds every execution until it is closed or the
	// execution is cancelled; started receives once per execution.
	gate    *chan struct{}
	started chan struct{}
}

func (seriesProvider) Type() string { return "series-mock" }

func (p seriesProvider) Execute(ctx dbcontext.Context, req query.ProviderRequest) ([]query.Row, error) {
	p.mu.Lock()
	*p.requests = append(*p.requests, req)
	*p.envs = append(*p.envs, ctx.Value(seriesEnvKey{}))
	p.mu.Unlock()
	if *p.gate != nil {
		p.started <- struct{}{}
		select {
		case <-*p.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	window := req.Options["range"].(map[string]any)
	rows := []query.Row{}
	for _, at := range []string{window["start"].(string), window["end"].(string)} {
		for i, pod := range []string{"api", "web"} {
			rows = append(rows, query.Row{"time": at, "pod": pod, "namespace": req.Params["namespace"], "value": float64(i + 1)})
		}
	}
	return rows, nil
}

// seriesEnvKey is the request value the series specs name an environment by,
// as an embedder's middleware would.
type seriesEnvKey struct{}

func inSeriesEnv(environment string) context.Context {
	return context.WithValue(context.Background(), seriesEnvKey{}, environment)
}

func seriesEnvOf(ctx context.Context) (string, error) {
	environment, ok := ctx.Value(seriesEnvKey{}).(string)
	if !ok {
		return "", fmt.Errorf("the request names no environment")
	}
	return environment, nil
}

type seriesAnswer struct {
	Series []clickymetrics.Series
	Err    error
}

var _ = Describe("ProfileSource", func() {
	var (
		mu       sync.Mutex
		requests []query.ProviderRequest
		envs     []any
		gate     chan struct{}
		started  chan struct{}
		reads    [][]profilestore.ReadRequest
		hookErr  error
		source   clickymetrics.SeriesSource
	)
	profile := query.Profile{
		Name:     "pod-cpu",
		Provider: query.ProviderConfig{Type: "series-mock", Options: map[string]any{"range": map[string]any{"start": "now-1h", "end": "now", "step": "1m"}}},
		Params:   []query.ParamDef{{Name: "namespace", Required: true}},
		Series:   &query.SeriesSpec{Labels: []string{"pod"}, Unit: "percent"},
	}
	since := time.Date(2024, 1, 1, 0, 0, 40, 0, time.UTC)
	until := time.Date(2024, 1, 1, 0, 2, 50, 0, time.UTC)
	start, end := time.Date(2024, 1, 1, 0, 0, 30, 0, time.UTC), time.Date(2024, 1, 1, 0, 2, 30, 0, time.UTC)

	BeforeEach(func() {
		requests, envs, gate, started, reads, hookErr = nil, nil, nil, make(chan struct{}, 4), nil, nil
		query.RegisterProvider(seriesProvider{mu: &mu, requests: &requests, envs: &envs, gate: &gate, started: started})
		store, err := NewFileStore(GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		Expect(store.Save(context.Background(), profile)).To(Succeed())
		service, err := New(Options{
			Store:      func() (profilestore.Store, error) { return store, nil },
			Context:    func() dbcontext.Context { return dbcontext.New() },
			DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
			BeforeExecute: func(_ context.Context, batch []profilestore.ReadRequest) (func(), error) {
				mu.Lock()
				reads = append(reads, batch)
				mu.Unlock()
				return func() {}, hookErr
			},
		})
		Expect(err).ToNot(HaveOccurred())
		source, err = service.SeriesSource(SeriesSourceOptions{Scope: seriesEnvOf, Timeout: time.Minute})
		Expect(err).ToNot(HaveOccurred())
	})

	It("expands one id into a series per label set over the step-aligned window", func() {
		id := "profile:pod-cpu?namespace=acme"
		series, err := source.QuerySeries(inSeriesEnv("acme-prod"), clickymetrics.SeriesRequest{IDs: []string{id}, Since: since, Until: until, Step: 30 * time.Second})
		Expect(err).ToNot(HaveOccurred())
		Expect(series).To(Equal([]clickymetrics.Series{
			{ID: id + "#pod=api", Source: id, Labels: map[string]string{"pod": "api"}, Unit: "percent", Points: []clickymetrics.Point{{At: start, Value: 1}, {At: end, Value: 1}}},
			{ID: id + "#pod=web", Source: id, Labels: map[string]string{"pod": "web"}, Unit: "percent", Points: []clickymetrics.Point{{At: start, Value: 2}, {At: end, Value: 2}}},
		}))
		Expect(requests).To(HaveLen(1))
		Expect(requests[0].Options["range"]).To(Equal(map[string]any{"start": "2024-01-01T00:00:30Z", "end": "2024-01-01T00:02:30Z", "step": "30s"}))
		Expect(reads).To(HaveLen(1))
		Expect(reads[0][0].Params).To(Equal(map[string]any{"namespace": "acme"}))
	})

	It("runs identical ids once per tick", func() {
		ids := []string{"profile:pod-cpu?namespace=acme", "profile:pod-cpu?namespace=acme"}
		series, err := source.QuerySeries(inSeriesEnv("acme-prod"), clickymetrics.SeriesRequest{IDs: ids, Since: since, Until: until, Step: 30 * time.Second})
		Expect(err).ToNot(HaveOccurred())
		Expect(series).To(HaveLen(2))
		Expect(requests).To(HaveLen(1))
	})

	It("runs an identical id once per environment", func() {
		request := clickymetrics.SeriesRequest{IDs: []string{"profile:pod-cpu?namespace=acme"}, Since: since, Until: until, Step: 30 * time.Second}
		for _, environment := range []string{"acme-prod", "acme-test"} {
			_, err := source.QuerySeries(inSeriesEnv(environment), request)
			Expect(err).ToNot(HaveOccurred())
		}
		Expect(envs).To(Equal([]any{"acme-prod", "acme-test"}))
	})

	It("keeps a shared execution running for the callers still waiting when the first one leaves", func() {
		gate = make(chan struct{})
		request := clickymetrics.SeriesRequest{IDs: []string{"profile:pod-cpu?namespace=acme"}, Since: since, Until: until, Step: 30 * time.Second}
		ask := func(ctx context.Context) chan seriesAnswer {
			answer := make(chan seriesAnswer, 1)
			go func() {
				series, err := source.QuerySeries(ctx, request)
				answer <- seriesAnswer{Series: series, Err: err}
			}()
			return answer
		}
		first, leave := context.WithCancel(inSeriesEnv("acme-prod"))
		defer leave()
		firstAnswer := ask(first)
		Eventually(started).Should(Receive())
		secondAnswer := ask(inSeriesEnv("acme-prod"))
		Eventually(func() int { mu.Lock(); defer mu.Unlock(); return len(reads) }).Should(Equal(2))

		leave()
		Eventually(firstAnswer).Should(Receive(HaveField("Err", MatchError(context.Canceled))))
		close(gate)
		var second seriesAnswer
		Eventually(secondAnswer).Should(Receive(&second))
		Expect(second.Err).ToNot(HaveOccurred())
		Expect(second.Series).To(HaveLen(2))
		Expect(requests).To(HaveLen(1))
	})

	It("refuses a read whose environment it cannot name", func() {
		_, err := source.QuerySeries(context.Background(), clickymetrics.SeriesRequest{IDs: []string{"profile:pod-cpu?namespace=acme"}, Since: since, Until: until, Step: 30 * time.Second})
		Expect(err).To(MatchError(ContainSubstring("names no environment")))
		Expect(requests).To(BeEmpty())
	})

	It("refuses a read the caller may not make, before querying", func() {
		hookErr = fmt.Errorf("namespace acme: %w", profilestore.ErrProfileForbidden)
		_, err := source.QuerySeries(inSeriesEnv("acme-prod"), clickymetrics.SeriesRequest{IDs: []string{"profile:pod-cpu?namespace=acme"}, Since: since, Until: until, Step: 30 * time.Second})
		Expect(err).To(MatchError(profilestore.ErrProfileForbidden))
		Expect(requests).To(BeEmpty())
	})

	DescribeTable("rejects ids and requests it cannot serve",
		func(id string, step time.Duration, message string) {
			_, err := source.QuerySeries(inSeriesEnv("acme-prod"), clickymetrics.SeriesRequest{IDs: []string{id}, Since: since, Until: until, Step: step})
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("no step", "profile:pod-cpu?namespace=acme", time.Duration(0), "step"),
		Entry("another prefix", "task:cpu", 30*time.Second, `"profile:"`),
		Entry("unknown profile", "profile:missing", 30*time.Second, "missing"),
	)
})
