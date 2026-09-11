package query_test

import (
	"reflect"

	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

// arrayProfile describes a backend field that carries several string values.
func arrayProfile(provider string, filter *query.ColumnFilterDef) query.Profile {
	return query.Profile{
		Name:     "events",
		Provider: query.ProviderConfig{Type: provider},
		Query:    "select tables from events",
		Columns: []query.ColumnDef{
			{Name: "tables", Type: query.ColumnTypeJSON, Source: "tables", JSONPath: "$", Filter: filter},
		},
	}
}

var _ = Describe("array column filters", func() {
	arrayFilter := &query.ColumnFilterDef{Kind: query.ColumnFilterKindTerms, Array: true}

	DescribeTable("binds array elements on every provider with element semantics",
		func(provider string) {
			bindings, err := arrayProfile(provider, arrayFilter).ColumnFilterBindings()
			Expect(err).ToNot(HaveOccurred())
			Expect(bindings).To(Equal([]query.ColumnFilterBinding{{
				Column: "tables", Key: "filter.tables", Field: "tables", Label: "tables",
				Kind: query.ColumnFilterKindTerms, Array: true, Lookup: true, Multi: true,
			}}))
		},
		Entry("generic SQL", "sql"),
		Entry("PostgreSQL", "postgres"),
		Entry("MySQL", "mysql"),
		Entry("SQL Server", "sqlserver"),
		Entry("ClickHouse", "clickhouse"),
		Entry("SQLite", "sqlite"),
		Entry("OpenSearch", "opensearch"),
		Entry("OpenTelemetry", "opentelemetry"),
	)

	It("carries the array form onto every selection a request makes", func() {
		filters, err := query.ResolveColumnFilters(arrayProfile("sql", arrayFilter), map[string]any{"filter.tables": "policy,!client"})
		Expect(err).ToNot(HaveOccurred())
		Expect(filters).To(Equal([]query.ColumnFilterValue{{
			Column: "tables", Key: "filter.tables", Field: "tables", Kind: query.ColumnFilterKindTerms, Array: true,
			Include: []string{"policy"}, Exclude: []string{"client"},
		}}))
	})

	DescribeTable("permits an array inside a document provider's nested entry",
		func(provider string) {
			profile := arrayProfile(provider, &query.ColumnFilterDef{
				Field: "tags.values", Nested: "tags", Array: true,
			})
			profile.Columns[0].Name = "values"
			bindings, err := profile.ColumnFilterBindings()
			Expect(err).ToNot(HaveOccurred())
			Expect(bindings).To(ConsistOf(And(
				HaveField("Field", "tags.values"),
				HaveField("Nested", "tags"),
				HaveField("Array", true),
			)))
		},
		Entry("OpenSearch", "opensearch"),
		Entry("OpenTelemetry", "opentelemetry"),
	)

	It("does not advertise array filtering for providers without element semantics", func() {
		Expect(query.SupportsArrayFilters("http")).To(BeFalse())
	})

	It("refuses SQL nesting even when the selected field is an array", func() {
		profile := arrayProfile("sqlite", &query.ColumnFilterDef{
			Field: "tags.values", Nested: "tags", Array: true,
		})
		_, err := profile.ColumnFilterBindings()
		Expect(err).To(MatchError(ContainSubstring(`provider "sqlite" has no equivalent`)))
	})

	It("validates an explicitly declared SQLite field as a plain result column", func() {
		_, err := arrayProfile("sqlite", &query.ColumnFilterDef{
			Field: "json_each(tables)", Array: true,
		}).ColumnFilterBindings()
		Expect(err).To(MatchError(ContainSubstring(`is not a plain column name`)))
	})

	DescribeTable("refuses an array declaration no element comparison could honour",
		func(def query.ColumnFilterDef, message string) {
			Expect(def.Validate("tables")).To(MatchError(ContainSubstring(message)))
		},
		Entry("a range over the elements", query.ColumnFilterDef{Kind: query.ColumnFilterKindRange, Array: true}, `array filter requires a "terms" filter`),
		Entry("a substring match", query.ColumnFilterDef{Kind: query.ColumnFilterKindText, Array: true}, `array filter requires a "terms" filter`),
	)

	Describe("ColumnsFor", func() {
		type tagged struct {
			Tables []string `json:"tables"`
			Hosts  []string `json:"hosts" filter:"limit=20"`
			Notes  []string `json:"notes" filter:"-"`
		}

		It("gives a string list a value selection over its elements", func() {
			columns, err := query.ColumnsFor(reflect.TypeFor[tagged]())
			Expect(err).ToNot(HaveOccurred())
			Expect(columns).To(Equal([]query.ColumnDef{
				{Name: "tables", Type: query.ColumnTypeJSON, Filter: &query.ColumnFilterDef{Kind: query.ColumnFilterKindTerms, Array: true}},
				{Name: "hosts", Type: query.ColumnTypeJSON, Filter: &query.ColumnFilterDef{Kind: query.ColumnFilterKindTerms, Array: true, Limit: lo.ToPtr(20)}},
				{Name: "notes", Type: query.ColumnTypeJSON, Filter: &query.ColumnFilterDef{Disabled: true}},
			}))
		})

		It("refuses a string list declared to filter as something other than its elements' values", func() {
			_, err := query.ColumnsFor(reflect.TypeFor[struct {
				Tables []string `json:"tables" filter:"text"`
			}]())
			Expect(err).To(MatchError(ContainSubstring(`a string list filters by its elements' values; kind "text"`)))
		})
	})
})
