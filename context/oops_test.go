package context

import (
	gocontext "context"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/oops"
	"go.opentelemetry.io/otel/trace"
)

var _ = ginkgo.Describe("Context.Oops", func() {
	ginkgo.It("binds the active OpenTelemetry trace to the error", func() {
		traceID, err := trace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
		Expect(err).NotTo(HaveOccurred())
		spanID, err := trace.SpanIDFromHex("0123456789abcdef")
		Expect(err).NotTo(HaveOccurred())
		spanContext := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled})
		ctx := NewContext(trace.ContextWithSpanContext(gocontext.Background(), spanContext))

		oopsError, ok := oops.AsOops(ctx.Oops("query").Errorf("lookup failed"))

		Expect(ok).To(BeTrue())
		Expect(oopsError.Trace()).To(Equal(traceID.String()))
	})
})
