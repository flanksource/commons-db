package providers

import (
	"fmt"

	opensearchinspect "github.com/flanksource/commons-db/inspect/opensearch"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("column filter statistics", func() {
	It("counts all SQL string outputs in one wrapped statement", func() {
		statement, err := buildColumnStatsSQL(dialectPostgres, ordersQuery, []string{"message", "region"})
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(Equal(
			"WITH \"__cdb_base\" AS (\n" + ordersQuery + "\n)\n" +
				`SELECT COUNT(DISTINCT "message"), COUNT(DISTINCT "region")` + "\n" +
				`FROM "__cdb_base"`))
	})

	It("caps metadata probes to backend-safe field batches", func() {
		fields := make([]string, 126)
		for index := range fields {
			fields[index] = fmt.Sprintf("field_%d", index)
		}
		sqlBatches := fieldBatches(fields, sqlColumnStatsBatchSize)
		Expect(sqlBatches).To(HaveLen(6))
		for _, batch := range sqlBatches {
			Expect(len(batch)).To(BeNumerically("<=", sqlColumnStatsBatchSize))
		}
		openSearchBatches := fieldBatches(fields, openSearchColumnStatsBatchSize)
		Expect(openSearchBatches).To(HaveLen(3))
		for _, batch := range openSearchBatches {
			Expect(len(batch)).To(BeNumerically("<=", openSearchColumnStatsBatchSize))
		}
	})

	It("offers lists only for proven low-cardinality strings", func() {
		columns := []query.ColumnDef{
			{Name: "region", Type: query.ColumnTypeString},
			{Name: "message", Type: query.ColumnTypeString},
		}
		Expect(sqlFilterSuggestions(columns, map[string]int64{"region": 12, "message": 51})).To(Equal(
			map[string]*query.ColumnFilterDef{
				"message": {Kind: query.ColumnFilterKindText},
			}))
	})

	It("maps OpenTelemetry output columns back to their configured index fields", func() {
		options := openTelemetryOptions{
			ServiceField: "resource.service.name",
			StatusFields: []string{"span.status.code", "error"},
		}
		options.withDefaults()
		Expect(openTelemetryInspectionColumns([]query.ColumnDef{
			{Name: "timestamp"},
			{Name: "service"},
			{Name: "service_name"},
			{Name: "status"},
			{Name: "custom.attribute"},
		}, options)).To(Equal([]query.ColumnDef{
			{Name: "timestamp", Source: "@timestamp"},
			{Name: "service", Source: "resource.service.name"},
			{Name: "service_name", Source: "resource.service.name"},
			{Name: "status", Source: "span.status.code"},
			{Name: "custom.attribute"},
		}))
	})

	It("uses OpenSearch mappings and distinct counts instead of sampled rows", func() {
		columns := []query.ColumnDef{
			{Name: "service", Type: query.ColumnTypeString},
			{Name: "message", Type: query.ColumnTypeString},
			{Name: "trace_id", Type: query.ColumnTypeString},
			{Name: "mixed", Type: query.ColumnTypeString},
		}
		fields := []opensearchinspect.Field{
			{Name: "service", Types: []string{"text"}, Searchable: true},
			{Name: "service.keyword", Types: []string{"keyword"}, Searchable: true, Aggregatable: true},
			{Name: "message", Types: []string{"text"}, Searchable: true},
			{Name: "message.keyword", Types: []string{"keyword"}, Searchable: true, Aggregatable: true},
			{Name: "trace_id", Types: []string{"keyword"}, Searchable: true, Aggregatable: true},
			{Name: "mixed", Types: []string{"keyword", "text"}, Conflicting: true},
		}

		Expect(openSearchFilterSuggestions(columns, fields, map[string]int64{
			"service.keyword": 12,
			"message.keyword": 51,
			"trace_id":        51,
		})).To(Equal(map[string]*query.ColumnFilterDef{
			"service":  {Kind: query.ColumnFilterKindTerms, Field: "service.keyword"},
			"message":  {Kind: query.ColumnFilterKindText},
			"trace_id": {Kind: query.ColumnFilterKindExact},
			"mixed":    {Disabled: true},
		}))
	})

	It("treats a truncated OpenSearch terms probe as high cardinality", func() {
		counts, err := parseOpenSearchColumnStats(map[string]any{
			"__cdb_column_0": map[string]any{
				"buckets":             []any{map[string]any{"key": "one"}},
				"sum_other_doc_count": float64(7),
			},
		}, []string{"service"})
		Expect(err).ToNot(HaveOccurred())
		Expect(counts).To(Equal(map[string]int64{
			"service": query.DefaultFilterLookupLimit + 1,
		}))
	})
})
