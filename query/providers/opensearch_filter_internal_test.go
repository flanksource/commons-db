package providers

import (
	"github.com/flanksource/commons-db/query"
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
		applyOpenSearchFilters(body, []query.ColumnFilterValue{{
			Field: "region", Include: []string{"us-east", "us-west"},
		}})

		Expect(body).To(Equal(map[string]any{"query": map[string]any{"bool": map[string]any{
			"filter": []any{
				map[string]any{"match_all": map[string]any{}},
				map[string]any{"terms": map[string]any{"region": []any{"us-east", "us-west"}}},
			},
		}}}))
	})

	It("ANDs across distinct fields while OR-ing within each", func() {
		body := baseBody()
		applyOpenSearchFilters(body, []query.ColumnFilterValue{
			{Field: "region", Include: []string{"us-east", "us-west"}},
			{Field: "env", Include: []string{"prod"}},
		})

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
		applyOpenSearchFilters(body, []query.ColumnFilterValue{{
			Field: "region", Include: []string{"us-east"}, Exclude: []string{"eu", "ap"},
		}})

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
		applyOpenSearchFilters(body, []query.ColumnFilterValue{{
			Field: "region", Exclude: []string{"eu"},
		}})

		Expect(body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{map[string]any{"match_all": map[string]any{}}},
			"must_not": []any{
				map[string]any{"terms": map[string]any{"region": []any{"eu"}}},
			},
		}}))
	})

	It("merges two filters that bind the same backend field", func() {
		body := baseBody()
		applyOpenSearchFilters(body, []query.ColumnFilterValue{
			{Key: "filter.region", Field: "region", Include: []string{"us-east"}},
			{Key: "regions", Field: "region", Include: []string{"us-west"}},
		})

		Expect(body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{
				map[string]any{"match_all": map[string]any{}},
				map[string]any{"terms": map[string]any{"region": []any{"us-east", "us-west"}}},
			},
		}}))
	})

	It("leaves the body untouched when no filter carries a value", func() {
		body := baseBody()
		applyOpenSearchFilters(body, nil)
		Expect(body).To(Equal(baseBody()))
	})

	It("wraps a missing query in match_all so the filters still apply", func() {
		body := map[string]any{}
		applyOpenSearchFilters(body, []query.ColumnFilterValue{{Field: "env", Include: []string{"prod"}}})

		Expect(body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{
				map[string]any{"match_all": map[string]any{}},
				map[string]any{"terms": map[string]any{"env": []any{"prod"}}},
			},
		}}))
	})
})
