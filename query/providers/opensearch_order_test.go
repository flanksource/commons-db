package providers_test

import (
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// tiebreaker is the sort field a derived order ends in. It is repeated here
// rather than exported from the provider: a test that read the constant would
// pass just as happily if the constant changed to something OpenSearch rejects,
// or to something that only sorts inside a point-in-time.
const tiebreaker = "_id"

func opensearchConfig(options map[string]any) query.ProviderConfig {
	options["index"] = "logs-*"
	return query.ProviderConfig{Type: "opensearch", Options: options}
}

var _ = Describe("opensearch natural order", func() {
	DescribeTable("derives the order an unordered profile can be paged by",
		func(options map[string]any, expected query.Order) {
			order, err := query.NaturalOrder(opensearchConfig(options))
			Expect(err).ToNot(HaveOccurred())
			Expect(order).To(Equal(expected))
			if expected != nil {
				Expect(order.Validate()).To(Succeed())
				Expect(order.Pageable()).To(Succeed(),
					"a derived order that cannot be paged by is the one thing it exists to be")
			}
		},
		Entry("sorts by the time field, newest first, when the index declares one",
			map[string]any{"search": map[string]any{"timeField": "startTimeMillis"}},
			query.Order{
				{Column: "startTimeMillis", Desc: true},
				{Column: tiebreaker, Unique: true},
			}),
		Entry("keeps the specification's own sort and only adds the tiebreaker",
			map[string]any{"search": map[string]any{
				"timeField": "@timestamp",
				"sort": []any{
					map[string]any{"field": "severity", "order": "asc"},
					map[string]any{"field": "@timestamp", "order": "desc"},
				},
			}},
			query.Order{
				{Column: "severity"},
				{Column: "@timestamp", Desc: true},
				{Column: tiebreaker, Unique: true},
			}),
		Entry("treats an unstated sort direction as ascending, as OpenSearch does",
			map[string]any{"search": map[string]any{
				"sort": []any{map[string]any{"field": "service"}},
			}},
			query.Order{
				{Column: "service"},
				{Column: tiebreaker, Unique: true},
			}),
		Entry("declines a specification with neither a sort nor a time field",
			map[string]any{"search": map[string]any{
				"query": map[string]any{"op": "term", "field": "level", "value": "error"},
			}},
			nil),
		Entry("declines a raw-DSL profile, whose sort it does not parse",
			map[string]any{"limit": "500"},
			nil),
	)

	// A sort already ending in the tiebreaker would otherwise produce an order
	// that names it twice, which Validate rejects — so the profile would lose
	// paging by asking for exactly the right thing.
	It("does not repeat a tiebreaker the specification already sorts by", func() {
		order, err := query.NaturalOrder(opensearchConfig(map[string]any{"search": map[string]any{
			"sort": []any{
				map[string]any{"field": "@timestamp", "order": "desc"},
				map[string]any{"field": tiebreaker, "order": "asc"},
			},
		}}))
		Expect(err).ToNot(HaveOccurred())
		Expect(order).To(Equal(query.Order{
			{Column: "@timestamp", Desc: true},
			{Column: tiebreaker, Unique: true},
		}))
		Expect(order.Validate()).To(Succeed())
	})

	It("leaves a declared order alone", func() {
		declared := query.Order{{Column: "trace_id", Unique: true}}
		profile := query.Profile{
			Name:     "declared",
			Provider: opensearchConfig(map[string]any{"search": map[string]any{"timeField": "startTimeMillis"}}),
			Order:    declared,
		}
		order, err := profile.EffectiveOrder()
		Expect(err).ToNot(HaveOccurred())
		Expect(order).To(Equal(declared))
	})

	// The point of the whole derivation: a profile that declared nothing can be
	// asked for a position past its first page, which is what puts a pager in
	// front of the caller.
	It("makes an unordered profile pageable", func() {
		profile := query.Profile{
			Name:     "os2",
			Provider: opensearchConfig(map[string]any{"search": map[string]any{"timeField": "startTimeMillis"}}),
		}
		Expect(profile.Pageable()).To(Succeed())
	})

	It("leaves a profile with nothing to order by un-pageable, and says why", func() {
		profile := query.Profile{
			Name:     "unordered",
			Provider: opensearchConfig(map[string]any{"limit": "500"}),
		}
		Expect(profile.Pageable()).To(MatchError(ContainSubstring("no order is declared")))
	})

	// A provider that names no natural order must not acquire one, or a SQL
	// profile would start claiming a total order over a column nobody checked.
	It("offers no order for a provider that has none", func() {
		order, err := query.NaturalOrder(query.ProviderConfig{
			Type:    "sql",
			Options: map[string]any{"url": "postgres://localhost/db"},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(order).To(BeNil())
	})
})
