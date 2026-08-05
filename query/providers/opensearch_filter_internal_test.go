package providers

import (
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A selection is a set of alternatives, so the values one filter contributes
// must be OR-ed. Emitting one term clause per value ANDs them, which can never
// match — the failure a multi-select filter hits on its second click.
var _ = Describe("applyOpenSearchFilters", func() {
	baseBody := func() map[string]any {
		return map[string]any{"query": map[string]any{"match_all": map[string]any{}}}
	}

	It("ORs several included values for one field into a single terms clause", func() {
		body := baseBody()
		Expect(applyOpenSearchFilters(body, []query.ColumnFilterValue{{
			Field: "region", Include: []string{"us-east", "us-west"},
		}}, nil)).To(Succeed())

		Expect(body).To(Equal(map[string]any{"query": map[string]any{"bool": map[string]any{
			"filter": []any{
				map[string]any{"match_all": map[string]any{}},
				map[string]any{"terms": map[string]any{"region": []any{"us-east", "us-west"}}},
			},
		}}}))
	})

	It("ANDs across distinct fields while OR-ing within each", func() {
		body := baseBody()
		Expect(applyOpenSearchFilters(body, []query.ColumnFilterValue{
			{Field: "region", Include: []string{"us-east", "us-west"}},
			{Field: "env", Include: []string{"prod"}},
		}, nil)).To(Succeed())

		Expect(body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{
				map[string]any{"match_all": map[string]any{}},
				map[string]any{"terms": map[string]any{"region": []any{"us-east", "us-west"}}},
				map[string]any{"terms": map[string]any{"env": []any{"prod"}}},
			},
		}}))
	})

	It("routes excluded values to must_not as one terms clause per field", func() {
		body := baseBody()
		Expect(applyOpenSearchFilters(body, []query.ColumnFilterValue{{
			Field: "region", Include: []string{"us-east"}, Exclude: []string{"eu", "ap"},
		}}, nil)).To(Succeed())

		Expect(body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{
				map[string]any{"match_all": map[string]any{}},
				map[string]any{"terms": map[string]any{"region": []any{"us-east"}}},
			},
			"must_not": []any{
				map[string]any{"terms": map[string]any{"region": []any{"eu", "ap"}}},
			},
		}}))
	})

	It("keeps an exclude-only selection as a pure must_not", func() {
		body := baseBody()
		Expect(applyOpenSearchFilters(body, []query.ColumnFilterValue{{
			Field: "region", Exclude: []string{"eu"},
		}}, nil)).To(Succeed())

		Expect(body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{map[string]any{"match_all": map[string]any{}}},
			"must_not": []any{
				map[string]any{"terms": map[string]any{"region": []any{"eu"}}},
			},
		}}))
	})

	It("merges two filters that bind the same backend field", func() {
		body := baseBody()
		Expect(applyOpenSearchFilters(body, []query.ColumnFilterValue{
			{Key: "filter.region", Field: "region", Include: []string{"us-east"}},
			{Key: "regions", Field: "region", Include: []string{"us-west"}},
		}, nil)).To(Succeed())

		Expect(body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{
				map[string]any{"match_all": map[string]any{}},
				map[string]any{"terms": map[string]any{"region": []any{"us-east", "us-west"}}},
			},
		}}))
	})

	It("leaves the body untouched when no filter carries a value", func() {
		body := baseBody()
		Expect(applyOpenSearchFilters(body, nil, nil)).To(Succeed())
		Expect(body).To(Equal(baseBody()))
	})

	It("wraps a missing query in match_all so the filters still apply", func() {
		body := map[string]any{}
		Expect(applyOpenSearchFilters(body, []query.ColumnFilterValue{{Field: "env", Include: []string{"prod"}}}, nil)).To(Succeed())

		Expect(body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{
				map[string]any{"match_all": map[string]any{}},
				map[string]any{"terms": map[string]any{"env": []any{"prod"}}},
			},
		}}))
	})

	It("keeps a structural list include once and applies its native exclusions", func() {
		body := map[string]any{"query": map[string]any{
			"terms": map[string]any{"scheme.id": []any{"one", "two"}},
		}}
		Expect(applyOpenSearchFilters(body, []query.ColumnFilterValue{{
			Key: "schemes", Field: "scheme.id",
			Include: []string{"one", "two"}, Exclude: []string{"three"},
		}}, []esdsl.ParamUse{{Name: "schemes", Field: "scheme.id"}})).To(Succeed())

		Expect(body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{
				map[string]any{"terms": map[string]any{"scheme.id": []any{"one", "two"}}},
			},
			"must_not": []any{
				map[string]any{"terms": map[string]any{"scheme.id": []any{"three"}}},
			},
		}}))
	})

	It("rejects a native list field that disagrees with its structural mapping", func() {
		err := applyOpenSearchFilters(baseBody(), []query.ColumnFilterValue{{
			Key: "schemes", Field: "scheme.id", Include: []string{"one"},
		}}, []esdsl.ParamUse{{Name: "schemes", Field: "other.id"}})

		Expect(err).To(MatchError(ContainSubstring(
			`param "schemes" maps native field "scheme.id" but its query condition uses "other.id"`,
		)))
	})

	It("rejects more than one structural mapping for a native list parameter", func() {
		err := applyOpenSearchFilters(baseBody(), []query.ColumnFilterValue{{
			Key: "schemes", Field: "scheme.id", Include: []string{"one"},
		}}, []esdsl.ParamUse{
			{Name: "schemes", Field: "scheme.id"},
			{Name: "schemes", Field: "peer.scheme"},
		})

		Expect(err).To(MatchError(ContainSubstring(
			`param "schemes" has 2 structural query mappings; list parameters require exactly one`,
		)))
	})
})
