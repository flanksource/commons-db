package providers

import (
	"encoding/json"

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

	Describe("kinds beyond a value selection", func() {
		bounded := func(min, max *query.FilterBound) *query.FilterRange {
			return &query.FilterRange{Min: min, Max: max}
		}

		It("compiles a numeric range into one range clause", func() {
			body := baseBody()
			Expect(applyOpenSearchFilters(body, []query.ColumnFilterValue{{
				Field: "latency_ms", Kind: query.ColumnFilterKindRange,
				Range: bounded(
					&query.FilterBound{Value: float64(100), Inclusive: true},
					&query.FilterBound{Value: float64(500), Inclusive: false}),
			}}, nil)).To(Succeed())

			Expect(body["query"]).To(Equal(map[string]any{"bool": map[string]any{
				"filter": []any{
					map[string]any{"match_all": map[string]any{}},
					map[string]any{"range": map[string]any{"latency_ms": map[string]any{
						"gte": float64(100), "lt": float64(500),
					}}},
				},
			}}))
		})

		// Naming a format would stop OpenSearch reading "now-15m" as date math,
		// which is the whole point of passing the operand through unresolved.
		It("passes date math through without a format", func() {
			body := baseBody()
			Expect(applyOpenSearchFilters(body, []query.ColumnFilterValue{{
				Field: "@timestamp", Kind: query.ColumnFilterKindTime,
				Range: bounded(&query.FilterBound{Value: "now-15m", Inclusive: true}, nil),
			}}, nil)).To(Succeed())

			clause := body["query"].(map[string]any)["bool"].(map[string]any)["filter"].([]any)[1]
			bounds := clause.(map[string]any)["range"].(map[string]any)["@timestamp"].(map[string]any)
			Expect(bounds).To(Equal(map[string]any{"gte": "now-15m"}))
			Expect(bounds).ToNot(HaveKey("format"))
		})

		// A document missing the field is neither true nor false, so must_not
		// term:true would keep it — which is not what "no" was asked to mean.
		DescribeTable("compiles a yes/no toggle into a term clause, both arms",
			func(selected bool) {
				body := baseBody()
				Expect(applyOpenSearchFilters(body, []query.ColumnFilterValue{{
					Field: "deleted", Kind: query.ColumnFilterKindBoolean, Bool: &selected,
				}}, nil)).To(Succeed())

				boolQuery := body["query"].(map[string]any)["bool"].(map[string]any)
				Expect(boolQuery).ToNot(HaveKey("must_not"))
				Expect(boolQuery["filter"]).To(Equal([]any{
					map[string]any{"match_all": map[string]any{}},
					map[string]any{"term": map[string]any{"deleted": selected}},
				}))
			},
			Entry("yes", true),
			Entry("no", false),
		)

		It("compiles a substring selection into match clauses", func() {
			body := baseBody()
			Expect(applyOpenSearchFilters(body, []query.ColumnFilterValue{{
				Field: "message", Kind: query.ColumnFilterKindText,
				Include: []string{"timeout"}, Exclude: []string{"healthcheck"},
			}}, nil)).To(Succeed())

			boolQuery := body["query"].(map[string]any)["bool"].(map[string]any)
			Expect(boolQuery["filter"]).To(Equal([]any{
				map[string]any{"match_all": map[string]any{}},
				map[string]any{"match": map[string]any{"message": "timeout"}},
			}))
			Expect(boolQuery["must_not"]).To(Equal([]any{
				map[string]any{"match": map[string]any{"message": "healthcheck"}},
			}))
		})

		It("intersects two range filters bound to the same field", func() {
			body := baseBody()
			Expect(applyOpenSearchFilters(body, []query.ColumnFilterValue{
				{Field: "latency_ms", Kind: query.ColumnFilterKindRange,
					Range: bounded(&query.FilterBound{Value: float64(100), Inclusive: true}, nil)},
				{Field: "latency_ms", Kind: query.ColumnFilterKindRange,
					Range: bounded(nil, &query.FilterBound{Value: float64(500), Inclusive: true})},
			}, nil)).To(Succeed())

			Expect(body["query"].(map[string]any)["bool"].(map[string]any)["filter"]).To(Equal([]any{
				map[string]any{"match_all": map[string]any{}},
				map[string]any{"range": map[string]any{"latency_ms": map[string]any{
					"gte": float64(100), "lte": float64(500),
				}}},
			}))
		})

		It("refuses two lower bounds for one field", func() {
			err := applyOpenSearchFilters(baseBody(), []query.ColumnFilterValue{
				{Field: "latency_ms", Kind: query.ColumnFilterKindRange,
					Range: bounded(&query.FilterBound{Value: float64(100)}, nil)},
				{Field: "latency_ms", Kind: query.ColumnFilterKindRange,
					Range: bounded(&query.FilterBound{Value: float64(200)}, nil)},
			}, nil)
			Expect(err).To(MatchError(ContainSubstring("two lower bounds")))
		})

		It("refuses a field selected as both values and a range", func() {
			err := applyOpenSearchFilters(baseBody(), []query.ColumnFilterValue{
				{Field: "status", Kind: query.ColumnFilterKindTerms, Include: []string{"200"}},
				{Field: "status", Kind: query.ColumnFilterKindRange,
					Range: bounded(&query.FilterBound{Value: float64(500), Inclusive: true}, nil)},
			}, nil)
			Expect(err).To(MatchError(ContainSubstring("filtered as both")))
		})

		It("keeps the author's query as the first filter clause under any kind", func() {
			body := map[string]any{"query": map[string]any{"match": map[string]any{"body": "error"}}}
			Expect(applyOpenSearchFilters(body, []query.ColumnFilterValue{{
				Field: "latency_ms", Kind: query.ColumnFilterKindRange,
				Range: bounded(&query.FilterBound{Value: float64(100), Inclusive: true}, nil),
			}}, nil)).To(Succeed())

			filter := body["query"].(map[string]any)["bool"].(map[string]any)["filter"].([]any)
			Expect(filter[0]).To(Equal(map[string]any{"match": map[string]any{"body": "error"}}))
		})

		It("emits one clause per field in first-seen order across kinds", func() {
			body := baseBody()
			selected := true
			Expect(applyOpenSearchFilters(body, []query.ColumnFilterValue{
				{Field: "region", Kind: query.ColumnFilterKindTerms, Include: []string{"eu"}},
				{Field: "latency_ms", Kind: query.ColumnFilterKindRange,
					Range: bounded(&query.FilterBound{Value: float64(100), Inclusive: true}, nil)},
				{Field: "deleted", Kind: query.ColumnFilterKindBoolean, Bool: &selected},
			}, nil)).To(Succeed())

			Expect(body["query"].(map[string]any)["bool"].(map[string]any)["filter"]).To(Equal([]any{
				map[string]any{"match_all": map[string]any{}},
				map[string]any{"terms": map[string]any{"region": []any{"eu"}}},
				map[string]any{"range": map[string]any{"latency_ms": map[string]any{"gte": float64(100)}}},
				map[string]any{"term": map[string]any{"deleted": true}},
			}))
		})
	})
})

var _ = Describe("FilterOpenSearch", func() {
	const authored = `{"query":{"term":{"level":"error"}},"sort":[{"@timestamp":"desc"}]}`

	It("returns the body byte for byte when nothing is selected", func() {
		Expect(FilterOpenSearch(authored, nil)).To(Equal(authored))
		Expect(FilterOpenSearch(authored, []query.ColumnFilterValue{{Field: "region"}})).To(Equal(authored))
	})

	It("merges the selection beside the author's own clause", func() {
		filtered, err := FilterOpenSearch(authored, []query.ColumnFilterValue{{
			Field: "region", Kind: query.ColumnFilterKindTerms,
			Include: []string{"us-east"}, Exclude: []string{"eu"},
		}})
		Expect(err).ToNot(HaveOccurred())

		var body map[string]any
		Expect(json.Unmarshal([]byte(filtered), &body)).To(Succeed())
		Expect(body["sort"]).To(Equal([]any{map[string]any{"@timestamp": "desc"}}))
		Expect(body["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{
				map[string]any{"term": map[string]any{"level": "error"}},
				map[string]any{"terms": map[string]any{"region": []any{"us-east"}}},
			},
			"must_not": []any{map[string]any{"terms": map[string]any{"region": []any{"eu"}}}},
		}}))
	})

	// A blank body is the whole index, which is the one thing a filter can still
	// narrow — so it becomes a query rather than a decode error.
	It("narrows a blank body", func() {
		filtered, err := FilterOpenSearch("  ", []query.ColumnFilterValue{{
			Field: "region", Kind: query.ColumnFilterKindTerms, Include: []string{"eu"},
		}})
		Expect(err).ToNot(HaveOccurred())
		Expect(filtered).To(MatchJSON(`{"query":{"bool":{"filter":[
			{"match_all":{}},{"terms":{"region":["eu"]}}
		]}}}`))
	})

	It("refuses a body that is not a query", func() {
		_, err := FilterOpenSearch("not json", []query.ColumnFilterValue{{
			Field: "region", Kind: query.ColumnFilterKindTerms, Include: []string{"eu"},
		}})
		Expect(err).To(MatchError(ContainSubstring("decode OpenSearch query")))
	})
})

// A `nested` field's entries are indexed as separate documents. A flat clause on
// one matches no parent document at all, and a pair of flat clauses on a plain
// array of objects matches documents carrying the key on one entry and the value
// on another — so the wrapper is not a refinement, it is the difference between
// the right rows and no rows or the wrong ones.
var _ = Describe("nested column filters", func() {
	tagFilter := func(values ...string) query.ColumnFilterValue {
		return query.ColumnFilterValue{
			Field: "tags.value", Nested: "tags", Where: map[string]string{"tags.key": "app"},
			Kind: query.ColumnFilterKindTerms, Include: values,
		}
	}

	It("pins the entry beside the selection inside one nested query", func() {
		filtered, err := FilterOpenSearch("", []query.ColumnFilterValue{tagFilter("web", "api")})
		Expect(err).ToNot(HaveOccurred())
		Expect(filtered).To(MatchJSON(`{"query":{"bool":{"filter":[
			{"match_all":{}},
			{"nested":{"path":"tags","query":{"bool":{"filter":[
				{"term":{"tags.key":"app"}},
				{"terms":{"tags.value":["web","api"]}}
			]}}}}
		]}}}`))
	})

	// "not app=legacy" is a statement about the document: no entry of it is
	// app=legacy. Inverting inside the wrapper would ask for an entry that is not,
	// which every document with a second tag satisfies.
	It("inverts the whole wrapper for an exclusion", func() {
		excluded := tagFilter()
		excluded.Exclude = []string{"legacy"}
		filtered, err := FilterOpenSearch("", []query.ColumnFilterValue{excluded})
		Expect(err).ToNot(HaveOccurred())
		Expect(filtered).To(MatchJSON(`{"query":{"bool":{
			"filter":[{"match_all":{}}],
			"must_not":[{"nested":{"path":"tags","query":{"bool":{"filter":[
				{"term":{"tags.key":"app"}},
				{"terms":{"tags.value":["legacy"]}}
			]}}}}]
		}}}`))
	})

	// One entry cannot be keyed both "app" and "env", so folding these into one
	// wrapper would ask for a tag that cannot exist.
	It("gives two entries of one container a wrapper each", func() {
		env := tagFilter()
		env.Where = map[string]string{"tags.key": "env"}
		env.Include = []string{"prod"}
		filtered, err := FilterOpenSearch("", []query.ColumnFilterValue{tagFilter("web"), env})
		Expect(err).ToNot(HaveOccurred())
		Expect(filtered).To(MatchJSON(`{"query":{"bool":{"filter":[
			{"match_all":{}},
			{"nested":{"path":"tags","query":{"bool":{"filter":[
				{"term":{"tags.key":"app"}},{"terms":{"tags.value":["web"]}}
			]}}}},
			{"nested":{"path":"tags","query":{"bool":{"filter":[
				{"term":{"tags.key":"env"}},{"terms":{"tags.value":["prod"]}}
			]}}}}
		]}}}`))
	})

	It("merges two selections that pin the same entry", func() {
		second := tagFilter("api")
		filtered, err := FilterOpenSearch("", []query.ColumnFilterValue{tagFilter("web"), second})
		Expect(err).ToNot(HaveOccurred())
		Expect(filtered).To(MatchJSON(`{"query":{"bool":{"filter":[
			{"match_all":{}},
			{"nested":{"path":"tags","query":{"bool":{"filter":[
				{"term":{"tags.key":"app"}},{"terms":{"tags.value":["web","api"]}}
			]}}}}
		]}}}`))
	})

	It("leaves a flat selection beside a nested one unwrapped", func() {
		filtered, err := FilterOpenSearch("", []query.ColumnFilterValue{
			tagFilter("web"),
			{Field: "region", Kind: query.ColumnFilterKindTerms, Include: []string{"eu"}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(filtered).To(MatchJSON(`{"query":{"bool":{"filter":[
			{"match_all":{}},
			{"nested":{"path":"tags","query":{"bool":{"filter":[
				{"term":{"tags.key":"app"}},{"terms":{"tags.value":["web"]}}
			]}}}},
			{"terms":{"region":["eu"]}}
		]}}}`))
	})

	It("wraps a range the same way a value selection is wrapped", func() {
		filtered, err := FilterOpenSearch("", []query.ColumnFilterValue{{
			Field: "tags.weight", Nested: "tags", Where: map[string]string{"tags.key": "load"},
			Kind:  query.ColumnFilterKindRange,
			Range: &query.FilterRange{Min: &query.FilterBound{Value: 3.0, Inclusive: true}},
		}})
		Expect(err).ToNot(HaveOccurred())
		Expect(filtered).To(MatchJSON(`{"query":{"bool":{"filter":[
			{"match_all":{}},
			{"nested":{"path":"tags","query":{"bool":{"filter":[
				{"term":{"tags.key":"load"}},
				{"range":{"tags.weight":{"gte":3}}}
			]}}}}
		]}}}`))
	})

	It("refuses a field its container does not hold", func() {
		_, err := FilterOpenSearch("", []query.ColumnFilterValue{{
			Field: "labels.app", Nested: "tags", Kind: query.ColumnFilterKindTerms, Include: []string{"web"},
		}})
		Expect(err).To(MatchError(ContainSubstring(`field "labels.app" is not inside nested "tags"`)))
	})
})
