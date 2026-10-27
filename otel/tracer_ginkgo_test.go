package otel

import (
	"context"
	"errors"
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

type recordingProcessor struct {
	started, ended int
}

func (p *recordingProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) { p.started++ }
func (p *recordingProcessor) OnEnd(sdktrace.ReadOnlySpan)                     { p.ended++ }
func (p *recordingProcessor) Shutdown(context.Context) error                  { return nil }
func (p *recordingProcessor) ForceFlush(context.Context) error                { return nil }

var _ = Describe("Tracer", func() {
	It("calls registered processors for global spans", func() {
		previous := api.GetTracerProvider()
		DeferCleanup(api.SetTracerProvider, previous)
		processor := &recordingProcessor{}
		exporter := tracetest.NewInMemoryExporter()
		shutdown, err := (Tracer{ServiceName: "test", Exporter: exporter, SpanProcessors: []sdktrace.SpanProcessor{processor}}).Init()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(shutdown)
		_, span := api.Tracer("test").Start(context.Background(), "operation")
		span.End()
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
