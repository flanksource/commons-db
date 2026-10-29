package processor_test

import (
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/processor"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("merge metric workspace", func() {
	It("retains time series gap filling in its private database", func() {
		rows, err := processor.Merge(context.New(),
			`SELECT grid.time, coalesce(samples.value, 0) AS value
			 FROM time_series('2024-01-01T00:00:00Z', '2024-01-01T00:03:00Z', '1m') AS grid
			 LEFT JOIN samples ON samples.time = grid.time ORDER BY grid.time`,
			processor.ResultSet{Name: "samples", Rows: []query.Row{{"time": "2024-01-01T00:01:00.000000000Z", "value": float64(10)}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(Equal([]query.Row{
			{"time": "2024-01-01T00:00:00.000000000Z", "value": int64(0)},
			{"time": "2024-01-01T00:01:00.000000000Z", "value": float64(10)},
			{"time": "2024-01-01T00:02:00.000000000Z", "value": int64(0)},
		}))
	})
})
