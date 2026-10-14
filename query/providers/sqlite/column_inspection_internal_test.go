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
