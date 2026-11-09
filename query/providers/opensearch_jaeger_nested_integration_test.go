package providers_test

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
)

// A Jaeger span index whose tags were never flattened keeps every attribute in a
// nested key/value list. A canonical profile still has to filter, sort and offer
// facet values on those attributes, and each of those is a claim about what
// OpenSearch does with a nested clause, so it is measured against a real cluster.
//
// Set COMMONS_DB_OPENSEARCH_URL to run these.
var _ = Describe("canonical attributes over Jaeger nested tags (live)", Ordered, func() {
	const index = "commons-db-jaeger-nested-it"

	var (
		address string
		ctx     dbcontext.Context
		profile query.Profile
	)

	spanIDs := func(rows []query.Row) []string {
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, fmt.Sprint(row["span_id"]))
		}
		return ids
	}

	BeforeAll(func() {
		address = strings.TrimSpace(os.Getenv("COMMONS_DB_OPENSEARCH_URL"))
		if address == "" {
			Skip("COMMONS_DB_OPENSEARCH_URL is not set")
		}
		ctx = dbcontext.New()
		tags := `{"type":"nested","properties":{"key":{"type":"keyword"},"type":{"type":"keyword"},"value":{"type":"keyword"}}}`
		_ = openSearchAdmin(address, http.MethodDelete, "/"+index, "")
		Expect(openSearchAdmin(address, http.MethodPut, "/"+index, `{"mappings":{"properties":{
			"traceID":{"type":"keyword"},"spanID":{"type":"keyword"},"operationName":{"type":"keyword"},
			"startTimeMillis":{"type":"date","format":"epoch_millis"},"startTime":{"type":"long"},"duration":{"type":"long"},
			"tags":`+tags+`,
			"process":{"properties":{"serviceName":{"type":"keyword"},"tags":`+tags+`}}
		}}}`)).To(Succeed())
		span := func(id, operation, dbName string) string {
			return fmt.Sprintf(`{"traceID":"t-%[1]s","spanID":"%[1]s","operationName":"query","startTimeMillis":1793613600000,"startTime":1793613600000000,"duration":250,`+
				`"tags":[{"key":"db.operation","type":"string","value":"%[2]s"},{"key":"db.name","type":"string","value":"%[3]s"}],`+
				`"process":{"serviceName":"oipa","tags":[{"key":"host.name","type":"string","value":"node-%[1]s"}]}}`, id, operation, dbName)
		}
		Expect(openSearchAdmin(address, http.MethodPost, "/_bulk?refresh=true", strings.Join([]string{
			`{"index":{"_index":"` + index + `","_id":"a"}}`, span("a", "SELECT", "alpha"),
			`{"index":{"_index":"` + index + `","_id":"b"}}`, span("b", "SELECT", "charlie"),
			`{"index":{"_index":"` + index + `","_id":"c"}}`, span("c", "CALL", "bravo"),
			"",
		}, "\n"))).To(Succeed())

		profile = query.Profile{
			Name:     "jaeger-nested",
			Render:   query.RenderTraces,
			Provider: query.ProviderConfig{Type: "opensearch", Options: map[string]any{"address": address, "index": index, "limit": "10"}},
			Search:   &esdsl.Search{},
			Columns: append(query.DefaultSpanColumns(),
				query.ColumnDef{Name: "attributes.db.operation", Source: "_span_attributes", JSONPath: `$["db.operation"]`, Filter: &query.ColumnFilterDef{Kind: "terms"}},
				query.ColumnDef{Name: "attributes.db.name", Source: "_span_attributes", JSONPath: `$["db.name"]`},
			),
		}
	})

	AfterAll(func() {
		if address != "" {
			_ = openSearchAdmin(address, http.MethodDelete, "/"+index, "")
		}
	})

	It("filters spans by an attribute held only in the nested tag list", func() {
		result, err := query.Execute(ctx, profile, map[string]any{"filter.attributes.db.operation": "SELECT"})
		Expect(err).ToNot(HaveOccurred())
		Expect(spanIDs(result.Rows)).To(ConsistOf("a", "b"))

		excluded, err := query.Execute(ctx, profile, map[string]any{"filter.attributes.db.operation": "!SELECT"})
		Expect(err).ToNot(HaveOccurred())
		Expect(spanIDs(excluded.Rows)).To(ConsistOf("c"))
	})

	It("sorts spans by an attribute held only in the nested tag list", func() {
		sorted := profile
		order, err := profile.RequestedOrder("attributes.db.name", true)
		Expect(err).ToNot(HaveOccurred())
		sorted.Order = order
		result, err := query.Execute(ctx, sorted, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(spanIDs(result.Rows)).To(Equal([]string{"b", "c", "a"}))
	})

	It("offers the pinned attribute's values counted by span", func() {
		options, _, err := query.LookupFilterValues(ctx, query.FilterValueLookupRequest{Profile: profile, Key: "filter.attributes.db.operation", Limit: 10})
		Expect(err).ToNot(HaveOccurred())
		counts := map[string]int64{}
		for _, option := range options {
			counts[option.Value] = option.Count
		}
		// db.name's values must not leak in: the lookup pins the db.operation entry.
		Expect(counts).To(Equal(map[string]int64{"SELECT": 2, "CALL": 1}))
	})
})
