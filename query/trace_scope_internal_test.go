package query

import (
	"time"

	"github.com/flanksource/commons-db/query/esdsl"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Trace scope", func() {
	const traceID = "92e8ccd4edb5839406a04d963a1d7a14"

	canonical := func() Profile {
		return Profile{
			Name:     "spans",
			Render:   RenderTraces,
			Provider: ProviderConfig{Type: "opensearch"},
			Search:   &esdsl.Search{},
			Columns:  DefaultSpanColumns(),
		}
	}

	scoped := func(profile Profile, input map[string]any) []ColumnFilterValue {
		_, filters, err := resolveProfileInput(profile, input, time.Now())
		Expect(err).ToNot(HaveOccurred())
		return filters
	}

	It("narrows a canonical traces profile to one trace through its hidden trace id column", func() {
		Expect(scoped(canonical(), map[string]any{TraceScopeKey: traceID})).To(Equal([]ColumnFilterValue{{
			Column: "trace_id", Key: TraceScopeKey, Field: "trace_id",
			Kind: ColumnFilterKindExact.Normalized(), Include: []string{traceID}, Exclude: []string{},
		}}))
	})

	It("addresses the trace id through the profile's own span mapping when it has no canonical search", func() {
		profile := canonical()
		profile.Search = nil
		profile.Spans = SpanMappings{"traceId": {Source: "traceID"}}
		Expect(scoped(profile, map[string]any{TraceScopeKey: traceID})).To(ConsistOf(
			HaveField("Field", "traceID"),
		))
	})

	It("is a request scope, not a control the filter bar offers", func() {
		bindings, err := canonical().FilterBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).ToNot(ContainElement(HaveField("Key", TraceScopeKey)))
	})

	It("is refused on a profile that does not render traces", func() {
		profile := canonical()
		profile.Render = ""
		profile.Search = nil
		_, _, err := resolveProfileInput(profile, map[string]any{TraceScopeKey: traceID}, time.Now())
		Expect(err).To(MatchError(ContainSubstring(`column filter "filter.trace_id" is not supported`)))
	})
})
