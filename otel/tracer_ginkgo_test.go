package otel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	api "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTracer(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Tracer Suite")
}

// collector records the URL path of every OTLP export it receives.
type collector struct {
	mu    sync.Mutex
	paths []string
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.paths = append(c.paths, r.URL.Path)
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (c *collector) received() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.paths...)
}

func emitSpan() {
	_, span := api.Tracer("test").Start(context.Background(), "operation")
	span.End()
}

type recordingProcessor struct {
	started, ended int
}

func (p *recordingProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) { p.started++ }
func (p *recordingProcessor) OnEnd(sdktrace.ReadOnlySpan)                     { p.ended++ }
func (p *recordingProcessor) Shutdown(context.Context) error                  { return nil }
func (p *recordingProcessor) ForceFlush(context.Context) error                { return nil }

var _ = Describe("Tracer", func() {
	var sink *collector
	var server *httptest.Server

	BeforeEach(func() {
		DeferCleanup(api.SetTracerProvider, api.GetTracerProvider())
		sink = &collector{}
		server = httptest.NewServer(sink)
		DeferCleanup(server.Close)
	})

	DescribeTable("Start flushes buffered spans to the collector on shutdown",
		func(path, wantPath string) {
			shutdown, err := (Tracer{ServiceName: "test", CollectorURL: server.URL + path}).Start(context.Background())
			Expect(err).NotTo(HaveOccurred())
			emitSpan()
			Expect(sink.received()).To(BeEmpty(), "batched spans are not exported before shutdown")
			Expect(shutdown(context.Background())).To(Succeed())
			Expect(sink.received()).To(Equal([]string{wantPath}))
		},
		Entry("base URL exports to /v1/traces", "", "/v1/traces"),
		Entry("explicit path is kept", "/custom/traces", "/custom/traces"),
	)

	It("Start returns an error for an unparseable collector URL", func() {
		shutdown, err := (Tracer{ServiceName: "test", CollectorURL: "http://bad host:4318"}).Start(context.Background())
		Expect(shutdown).To(BeNil())
		Expect(err).To(MatchError(ContainSubstring("invalid opentelemetry collector url")))
	})

	It("Init flushes spans on the returned shutdown", func() {
		shutdown, err := (Tracer{ServiceName: "test", CollectorURL: server.URL}).Init()
		Expect(err).NotTo(HaveOccurred())
		emitSpan()
		shutdown()
		Expect(sink.received()).To(Equal([]string{"/v1/traces"}))
	})

	It("Init returns setup errors", func() {
		shutdown, err := (Tracer{ServiceName: "test", CollectorURL: "http://bad host:4318"}).Init()
		Expect(shutdown).To(BeNil())
		Expect(err).To(MatchError(ContainSubstring("invalid opentelemetry collector url")))
	})

	It("calls registered processors for global spans", func() {
		processor := &recordingProcessor{}
		exporter := tracetest.NewInMemoryExporter()
		shutdown, err := (Tracer{ServiceName: "test", Exporter: exporter, SpanProcessors: []sdktrace.SpanProcessor{processor}}).Init()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(shutdown)
		emitSpan()
		Expect([]int{processor.started, processor.ended}).To(Equal([]int{1, 1}))
	})

	It("returns exporter and resource initialization errors", func() {
		failure := errors.New("initialization failed")
		for _, test := range []struct {
			name     string
			exporter func() (sdktrace.SpanExporter, error)
			resource func() (*resource.Resource, error)
		}{
			{"exporter", func() (sdktrace.SpanExporter, error) { return nil, failure }, func() (*resource.Resource, error) { return resource.Empty(), nil }},
			{"resource", func() (sdktrace.SpanExporter, error) { return tracetest.NewInMemoryExporter(), nil }, func() (*resource.Resource, error) { return nil, failure }},
		} {
			shutdown, err := (Tracer{}).init(test.exporter, test.resource)
			Expect(shutdown).To(BeNil())
			Expect(err).To(MatchError(ContainSubstring(test.name)))
			Expect(errors.Is(err, failure)).To(BeTrue())
		}
	})
})
