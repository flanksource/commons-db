package connections

import (
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL array column discovery", func() {
	DescribeTable("recognizes only supported native string arrays",
		func(databaseType string, columnType query.ColumnType, array bool) {
			storage := sqlColumnStorageOf(databaseType)
			Expect(storage.Type).To(Equal(columnType))
			Expect(storage.Array).To(Equal(array))
		},
		Entry("clickhouse string", "Array(String)", query.ColumnTypeJSON, true),
		Entry("clickhouse nullable string", "Array(Nullable(String))", query.ColumnTypeJSON, true),
		Entry("clickhouse low cardinality string", "Array(LowCardinality(String))", query.ColumnTypeJSON, true),
		Entry("clickhouse wrapped string", "Array(Nullable(LowCardinality(String)))", query.ColumnTypeJSON, true),
		Entry("postgres internal text array", "_TEXT", query.ColumnTypeJSON, true),
		Entry("postgres displayed text array", "TEXT[]", query.ColumnTypeJSON, true),
		Entry("scalar string", "String", query.ColumnTypeString, false),
		Entry("fixed-width string array", "Array(FixedString(16))", query.ColumnType(""), false),
		Entry("numeric array", "Array(Int64)", query.ColumnType(""), false),
		Entry("nested array", "Array(Array(String))", query.ColumnType(""), false),
		Entry("nullable outer array", "Nullable(Array(String))", query.ColumnType(""), false),
		Entry("json without an array declaration", "JSON", query.ColumnTypeJSON, false),
		Entry("jsonb without an array declaration", "JSONB", query.ColumnTypeJSON, false),
		Entry("SQL Server XML", "XML", query.ColumnTypeString, false),
		Entry("SQL Server unicode text", "NTEXT", query.ColumnTypeString, false),
	)

	It("preserves array filtering through the browser transport", func() {
		profile := query.Profile{Name: "connection browser", Provider: query.ProviderConfig{Type: "clickhouse"}, Columns: []query.ColumnDef{{
			Name: "tags",
			Type: query.ColumnTypeJSON,
			Filter: &query.ColumnFilterDef{
				Kind:  query.ColumnFilterKindTerms,
				Array: true,
			},
		}}}

		described, err := describeBrowserColumns(profile, map[string]string{"tags": "Array(String)"})
		Expect(err).ToNot(HaveOccurred())
		Expect(described).To(HaveLen(1))
		Expect(described[0].Filter).ToNot(BeNil())
		Expect(described[0].Filter.Array).To(BeTrue())

		roundTripped := browserColumnDefs(described)
		Expect(roundTripped).To(HaveLen(1))
		Expect(roundTripped[0].Filter).ToNot(BeNil())
		Expect(roundTripped[0].Filter.Array).To(BeTrue())
	})
})
