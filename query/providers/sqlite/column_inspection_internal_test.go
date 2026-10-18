package sqlite

import (
	"fmt"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers/internal/providerkit"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL column filter statistics", func() {
	It("counts all SQL string outputs in one wrapped statement", func() {
		statement, args, err := rendered(buildColumnStatsSQL(dialectPostgres, sqlQuery{Text: ordersQuery}, []string{"message", "region"}))
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(Equal(basePrefix(`"__cdb_base"`) +
			`SELECT COUNT(DISTINCT "message"), COUNT(DISTINCT "region") FROM "__cdb_base"`))
		Expect(args).To(BeEmpty())
	})

	It("binds the author's params into the counted base", func() {
		statement, args, err := rendered(buildColumnStatsSQL(dialectSQLServer, sqlQuery{
			Text: "SELECT region FROM orders WHERE env = " + sqlParamMarker(0), Args: []any{"prod"},
		}, []string{"region"}))
		Expect(err).ToNot(HaveOccurred())
		Expect(statement).To(Equal(
			"WITH [__cdb_base] AS (SELECT region AS region FROM orders WHERE env = @p1) SELECT COUNT(DISTINCT [region]) FROM [__cdb_base]"))
		Expect(args).To(Equal([]any{"prod"}))
	})

	It("caps metadata probes to backend-safe field batches", func() {
		fields := make([]string, 126)
		for index := range fields {
			fields[index] = fmt.Sprintf("field_%d", index)
		}
		batches := providerkit.FieldBatches(fields, sqlColumnStatsBatchSize)
		Expect(batches).To(HaveLen(6))
		for _, batch := range batches {
			Expect(len(batch)).To(BeNumerically("<=", sqlColumnStatsBatchSize))
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
})
