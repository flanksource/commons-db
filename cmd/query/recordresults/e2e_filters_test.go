package recordresults_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// expectedEvents counts run-1's events that keep passes.
func expectedEvents(keep func(sampleEvent) bool) int {
	count := 0
	for _, event := range sampleEvents(1, 250) {
		if keep(event) {
			count++
		}
	}
	return count
}

// expectedFacets is each value's row count in run-1, ordered as a lookup
// orders them: most rows first, then by value.
func expectedFacets(values func(sampleEvent) []string) []query.FilterOption {
	counts := map[string]int64{}
	for _, event := range sampleEvents(1, 250) {
		for _, value := range values(event) {
			counts[value]++
		}
	}
	options := make([]query.FilterOption, 0, len(counts))
	for value, count := range counts {
		options = append(options, query.FilterOption{Value: value, Count: count})
	}
	sort.Slice(options, func(i, j int) bool {
		if options[i].Count != options[j].Count {
			return options[i].Count > options[j].Count
		}
		return options[i].Value < options[j].Value
	})
	return options
}

// lookupFilter is one filter of the `__lookup=filters` JSON as the browser's
// filter bar reads it.
type lookupFilter struct {
	Options map[string]any `json:"options"`
	Counts  map[string]int `json:"counts"`
	Total   int            `json:"total"`
}

// lookupFilters asks the HTTP surface for run-1's filter values.
func (s resultServer) lookupFilters() map[string]lookupFilter {
	response := s.get(
		"/api/v1/profile/profile-trace-results-sample-event?stream=run-1&__lookup=filters", "application/json+clicky")
	Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
	var body struct {
		Filters map[string]lookupFilter `json:"filters"`
	}
	Expect(json.Unmarshal(response.Body.Bytes(), &body)).To(Succeed(), response.Body.String())
	return body.Filters
}

var _ = Describe("filtering a record result type by its columns", Ordered, func() {
	var server resultServer

	BeforeAll(func() { server = newResultServer() })

	// lookup asks the engine for filter's values over run-1, through the same
	// profile, index and hook the HTTP surface uses.
	lookup := func(filter string) []query.FilterOption {
		ctx := context.Background()
		profile, err := server.registry.Get(ctx, "trace-results/sample_event")
		Expect(err).ToNot(HaveOccurred())
		input := map[string]any{"stream": "run-1"}
		release, err := server.registry.BeforeExecute(ctx, []profiles.ReadRequest{{Profile: profile, Params: input}})
		Expect(err).ToNot(HaveOccurred())
		defer release()
		queryCtx := dbcontext.New().WithConnectionResolver(server.registry.ResolveConnection)
		options, total, err := query.LookupFilterValues(queryCtx, query.FilterValueLookupRequest{
			Profile: profile, Input: input, Key: filter, Limit: query.DefaultFilterLookupLimit,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(total.Value).To(BeEquivalentTo(len(options)))
		return options
	}

	DescribeTable("selects rows by the elements of a string list column",
		func(selection string, keep func(sampleEvent) bool) {
			expected := expectedEvents(keep)
			rows, header := server.rows("stream=run-1&limit=500&filter.tables=" + url.QueryEscape(selection))
			Expect(header.Get("X-Total-Count")).To(Equal(fmt.Sprint(expected)))
			Expect(rows).To(HaveLen(expected))
		},
		Entry("rows holding an element", "activity", func(event sampleEvent) bool {
			return slices.Contains(event.Tables, "activity")
		}),
		Entry("rows holding none of an element, including rows holding nothing", "!client", func(event sampleEvent) bool {
			return !slices.Contains(event.Tables, "client")
		}),
		Entry("rows holding one element and not another", "activity,!client", func(event sampleEvent) bool {
			return slices.Contains(event.Tables, "activity") && !slices.Contains(event.Tables, "client")
		}),
	)

	It("offers a string list column's elements, not its arrays, as its filter values", func() {
		tables := server.lookupFilters()["filter.tables"]
		Expect(tables.Total).To(Equal(3))
		Expect(tables.Options).To(And(HaveLen(3), HaveKey("policy"), HaveKey("client"), HaveKey("activity")))
	})

	// The counts are what let a filter bar say how many rows a value would keep
	// before the user picks it, so they have to survive the trip from the engine
	// to the JSON the browser reads, keyed by the same values as the options.
	DescribeTable("reports how many of the stream's rows hold each offered value",
		func(filter string, values func(sampleEvent) []string) {
			expected := map[string]int{}
			for _, facet := range expectedFacets(values) {
				expected[facet.Value] = int(facet.Count)
			}
			offered := server.lookupFilters()[filter]
			Expect(offered.Counts).To(Equal(expected))
			Expect(offered.Options).To(HaveLen(len(expected)))
		},
		Entry("a scalar column", "filter.db", func(event sampleEvent) []string { return []string{event.DB} }),
		Entry("a string list column, per element", "filter.tables", func(event sampleEvent) []string { return event.Tables }),
	)

	It("counts the rows of the stream holding each element", func() {
		Expect(lookup("filter.tables")).To(Equal(expectedFacets(func(event sampleEvent) []string { return event.Tables })))
	})

	It("counts the rows of the stream holding each value of a scalar column", func() {
		Expect(lookup("filter.db")).To(Equal(expectedFacets(func(event sampleEvent) []string { return []string{event.DB} })))
	})
})
