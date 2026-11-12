package layout_test

import (
	"encoding/json"
	"testing"

	"github.com/flanksource/commons-db/otel/span/layout"
	"github.com/flanksource/commons-db/query/esdsl"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestLayout(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Canonical span layouts") }

var _ = Describe("Portable span conditions", func() {
	It("rejects source enum ordering that differs from canonical ordering", func() {
		definition, err := layout.Discover(layout.Discovery{Fields: map[string]string{"traceID": "keyword", "spanID": "keyword", "startTimeMillis": "long", "duration": "long", "tag.error": "boolean"}})
		Expect(err).NotTo(HaveOccurred())
		for _, query := range []string{`{"query":{"range":{"status":{"gte":"error"}}}}`, `{"sort":[{"status":"asc"}]}`} {
			_, err := definition.Translate([]byte(query), layout.ToSource)
			Expect(err).To(HaveOccurred())
		}
	})
	It("preserves the existence requirement when importing a native false status", func() {
		definition, err := layout.Discover(layout.Discovery{Fields: map[string]string{"traceID": "keyword", "spanID": "keyword", "startTimeMillis": "long", "duration": "long", "tag.error": "boolean"}})
		Expect(err).NotTo(HaveOccurred())
		search, err := definition.TranslateSearch(esdsl.Search{Query: &esdsl.Condition{Op: esdsl.OpTerm, Field: "tag.error", Value: esdsl.Literal(false)}}, layout.ToCanonical)
		Expect(err).NotTo(HaveOccurred())
		compiled, err := esdsl.Compile(esdsl.CompileRequest{Search: search})
		Expect(err).NotTo(HaveOccurred())
		encoded, err := compiled.JSON()
		Expect(err).NotTo(HaveOccurred())
		Expect(encoded).To(MatchJSON(`{"query":{"bool":{"filter":[{"term":{"status":"unset"}},{"exists":{"field":"status"}}]}}}`))
	})
	It("uses keyword fields for exact conditions and document fields for source projections", func() {
		definition, err := layout.Discover(layout.Discovery{Fields: map[string]string{"traceID": "keyword", "spanID": "keyword", "startTimeMillis": "long", "duration": "long", "process.serviceName": "text", "process.serviceName.keyword": "keyword"}})
		Expect(err).NotTo(HaveOccurred())
		native, err := definition.Translate([]byte(`{"query":{"term":{"service":"checkout"}},"_source":["service"]}`), layout.ToSource)
		Expect(err).NotTo(HaveOccurred())
		Expect(native).To(MatchJSON(`{"query":{"term":{"process.serviceName.keyword":"checkout"}},"_source":["process.serviceName"]}`))
	})
	It("encodes and imports dates using the index mapping format", func() {
		definition, err := layout.Discover(layout.Discovery{Fields: map[string]string{"trace_id": "keyword", "span_id": "keyword", "timestamp": "date", "duration_ms": "double"}})
		Expect(err).NotTo(HaveOccurred())
		field := definition.Fields["timestamp"]
		field.Format = "epoch_millis"
		definition.Fields["timestamp"] = field
		logical := []byte(`{"query":{"range":{"timestamp":{"gte":"2026-11-02T10:00:00Z"}}}}`)
		native, err := definition.Translate(logical, layout.ToSource)
		Expect(err).NotTo(HaveOccurred())
		Expect(native).To(MatchJSON(`{"query":{"range":{"timestamp":{"gte":1793613600000}}}}`))
		restored, err := definition.Translate(native, layout.ToCanonical)
		Expect(err).NotTo(HaveOccurred())
		Expect(restored).To(MatchJSON(logical))
	})
	It("uses connection overrides for custom layouts and preserves nanosecond bounds", func() {
		definition, err := layout.Discover(layout.Discovery{Fields: map[string]string{"ids.trace": "keyword", "ids.span": "keyword", "clock": "long", "elapsed": "long", "workload": "keyword"}, Overrides: map[string]layout.Field{"trace_id": {Source: "ids.trace"}, "span_id": {Source: "ids.span"}, "timestamp": {Source: "clock", Unit: "ns"}, "duration_ms": {Source: "elapsed", Unit: "us"}, "service": {Source: "workload"}}})
		Expect(err).NotTo(HaveOccurred())
		canonical := []byte(`{"query":{"range":{"timestamp":{"gte":"2026-11-02T10:00:00.000000123Z"}}}}`)
		native, err := definition.Translate(canonical, layout.ToSource)
		Expect(err).NotTo(HaveOccurred())
		Expect(native).To(MatchJSON(`{"query":{"range":{"clock":{"gte":1793613600000000123}}}}`))
		restored, err := definition.Translate(native, layout.ToCanonical)
		Expect(err).NotTo(HaveOccurred())
		Expect(restored).To(MatchJSON(canonical))
		span, err := definition.Normalize(map[string]any{"ids.trace": "t", "ids.span": "s", "clock": "1793613600000000123", "elapsed": 2500, "workload": "checkout"})
		Expect(err).NotTo(HaveOccurred())
		Expect(span.DurationMs).To(Equal(2.5))
		Expect(span.StartedAt.Format("2006-01-02T15:04:05.000000000Z")).To(Equal("2026-11-02T10:00:00.000000123Z"))
	})
	It("preserves boolean status semantics including missing unset values", func() {
		definition, err := layout.Discover(layout.Discovery{Fields: map[string]string{"traceID": "keyword", "spanID": "keyword", "startTimeMillis": "long", "duration": "long", "tag.error": "boolean"}})
		Expect(err).NotTo(HaveOccurred())
		native, err := definition.Translate([]byte(`{"query":{"term":{"status":"error"}}}`), layout.ToSource)
		Expect(err).NotTo(HaveOccurred())
		Expect(native).To(MatchJSON(`{"query":{"term":{"tag.error":true}}}`))
		native, err = definition.Translate([]byte(`{"query":{"term":{"status":"unset"}}}`), layout.ToSource)
		Expect(err).NotTo(HaveOccurred())
		Expect(native).To(MatchJSON(`{"query":{"bool":{"should":[{"term":{"tag.error":false}},{"bool":{"must_not":[{"exists":{"field":"tag.error"}}]}}],"minimum_should_match":1}}}`))
		_, err = definition.Translate([]byte(`{"query":{"term":{"status":"ok"}}}`), layout.ToSource)
		Expect(err).To(HaveOccurred())
	})
	It("uses the same conditions across Jaeger and canonical indexes without mutating them", func() {
		logical := []byte(`{"query":{"bool":{"filter":[{"term":{"service":"checkout"}},{"range":{"duration_ms":{"gte":2.5}}},{"term":{"attributes.http.method":"GET"}}],"must_not":[{"term":{"operation":"health"}}]}},"sort":[{"timestamp":{"order":"desc"}}]}`)
		jaeger, err := layout.Discover(layout.Discovery{Fields: map[string]string{"traceID": "keyword", "spanID": "keyword", "startTimeMillis": "long", "duration": "long", "process.serviceName": "keyword", "operationName": "keyword", "tag.http@method": "keyword"}})
		Expect(err).NotTo(HaveOccurred())
		native, err := jaeger.Translate(logical, layout.ToSource)
		Expect(err).NotTo(HaveOccurred())
		Expect(native).To(MatchJSON(`{"query":{"bool":{"filter":[{"term":{"process.serviceName":"checkout"}},{"range":{"duration":{"gte":2500}}},{"term":{"tag.http@method":"GET"}}],"must_not":[{"term":{"operationName":"health"}}]}},"sort":[{"startTimeMillis":{"order":"desc"}}]}`))
		restored, err := jaeger.Translate(native, layout.ToCanonical)
		Expect(err).NotTo(HaveOccurred())
		Expect(restored).To(MatchJSON(logical))
		canonical, err := layout.Discover(layout.Discovery{Fields: map[string]string{"trace_id": "keyword", "span_id": "keyword", "timestamp": "date", "duration_ms": "double", "service": "keyword", "operation": "keyword", "attributes.http.method": "keyword"}})
		Expect(err).NotTo(HaveOccurred())
		second, err := canonical.Translate(logical, layout.ToSource)
		Expect(err).NotTo(HaveOccurred())
		Expect(second).To(MatchJSON(logical))
		Expect(json.Valid(logical)).To(BeTrue())
	})
	It("rejects conditions whose field has no source mapping", func() {
		mapping, err := layout.Discover(layout.Discovery{Fields: map[string]string{"traceID": "keyword", "spanID": "keyword", "startTimeMillis": "long", "duration": "long"}})
		Expect(err).NotTo(HaveOccurred())
		_, err = mapping.Translate([]byte(`{"query":{"term":{"service":"checkout"}}}`), layout.ToSource)
		Expect(err).To(MatchError(ContainSubstring("service")))
	})
})
