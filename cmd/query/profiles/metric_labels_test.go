package profiles

import (
	"context"

	"github.com/flanksource/clicky/rpc"

	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("metric label imports", func() {
	It("merges label declarations without mutating the imported profile", func() {
		base := query.Profile{Name: "base", Provider: query.ProviderConfig{Type: "prometheus"}, Labels: map[string]string{"pod": "pod", "namespace": "namespace"}}
		store := resolverStore{"base": base, "child": {Name: "child", Imports: []string{"base"}, Labels: map[string]string{"pod": "exported_pod"}}}
		resolved, err := Resolve(context.Background(), store, "child")
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Profile.Labels).To(Equal(map[string]string{"pod": "exported_pod", "namespace": "namespace"}))
		Expect(base.Labels["pod"]).To(Equal("pod"))
	})
})

var _ = Describe("metric profile API", func() {
	It("advertises statistic sorting and query metadata for both providers", func() {
		for _, provider := range []string{"prometheus", "cloudwatchmetrics"} {
			spec := &rpc.OpenAPISpec{Paths: map[string]rpc.OpenAPIPath{}, Clicky: &rpc.ClickySpecMeta{}}
			Expect(addProfileToSpec(spec, query.Profile{Name: "summary", Provider: query.ProviderConfig{Type: provider, Options: map[string]any{"language": "sql"}}, Query: `SELECT percentile(value,95) AS p95 FROM cpu`, Columns: []query.ColumnDef{{Name: "p95", Type: query.ColumnTypeNumber}}})).To(Succeed())
			operation := spec.Paths["/api/v1/profile/profile-summary"]["get"]
			found := false
			for _, p := range operation.Parameters {
				if p.Name == "sort" {
					found = true
				}
			}
			Expect(found).To(BeTrue())
			Expect(operation.Responses["200"].Headers).To(HaveKey("X-Metric-Metadata"))
		}
	})
})
