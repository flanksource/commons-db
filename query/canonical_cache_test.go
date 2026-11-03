package query

import (
	"github.com/flanksource/commons-db/query/esdsl"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Canonical value lookup cache", func() {
	It("separates conditions and reversible span mappings", func() {
		base := ProviderRequest{Provider: "opensearch"}
		original, err := filterValueCacheKey(filterValueCacheIdentity{}, base, FilterValueSearch{})
		Expect(err).NotTo(HaveOccurred())
		for _, changed := range []ProviderRequest{
			{Provider: "opensearch", Search: &esdsl.Search{Query: &esdsl.Condition{Op: esdsl.OpTerm, Field: "service", Value: esdsl.Literal("checkout")}}},
			{Provider: "opensearch", Spans: SpanMappings{"service": {Source: "workload"}}},
		} {
			key, err := filterValueCacheKey(filterValueCacheIdentity{}, changed, FilterValueSearch{})
			Expect(err).NotTo(HaveOccurred())
			Expect(key).NotTo(Equal(original))
		}
	})
})
