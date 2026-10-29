package otel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	api "go.opentelemetry.io/otel"
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

	It("Init keeps its signature and flushes spans on the returned shutdown", func() {
		shutdown := (Tracer{ServiceName: "test", CollectorURL: server.URL}).Init()
		emitSpan()
		shutdown()
		Expect(sink.received()).To(Equal([]string{"/v1/traces"}))
	})

	It("Init returns a no-op shutdown when setup fails", func() {
		shutdown := (Tracer{ServiceName: "test", CollectorURL: "http://bad host:4318"}).Init()
		Expect(shutdown).NotTo(BeNil())
		shutdown()
		Expect(sink.received()).To(BeEmpty())
	})
})
