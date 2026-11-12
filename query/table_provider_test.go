package query_test

import (
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type sampledRow struct {
	Name    string  `json:"name" filter:"terms" sort:"name"`
	CPU     float64 `json:"cpu" pretty:"type=duration,unit=ms" filter:"duration" sort:"cpu"`
	Enabled bool    `json:"enabled" filter:"boolean"`
	Missing *int    `json:"missing"`
	Count   int64   `json:"count"`
}

var _ = Describe("sampled table projection", func() {
	It("orders visible columns while preserving schema types and filter/sort bindings", func() {
		columns := query.TableColumnsFor[sampledRow]("cpu", "name")
		Expect(columns).To(HaveLen(5))
		Expect(columns[0]).To(SatisfyAll(HaveField("Name", "cpu"), HaveField("Unit", "ms"), HaveField("FilterKey", "filter.cpu"), HaveField("SortKey", "cpu"), HaveField("DefaultHidden", false)))
		Expect(columns[2]).To(HaveField("DefaultHidden", true))
	})
	It("keeps false zero null and integers beyond floating point precision native", func() {
		Expect(query.TableRowFor(sampledRow{Count: 9007199254740993})).To(Equal(map[string]any{"name": "", "cpu": float64(0), "enabled": false, "missing": nil, "count": int64(9007199254740993)}))
	})
})
