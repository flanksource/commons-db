package providers_test

import (
	"fmt"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Work items whose duration is unknown until they end: w2, w4 and w6 are still
// in flight, so a page of two ends both inside the null block and between the
// last duration and the first null.
const inFlightWorkItems = `SELECT column1 AS id, column2 AS durationMs FROM (VALUES
	('w1', 10), ('w2', NULL), ('w3', 30), ('w4', NULL), ('w5', 10), ('w6', NULL), ('w7', 20))`

func workItemsProfile() query.Profile {
	return query.Profile{
		Name:     "work-items",
		Provider: query.ProviderConfig{Type: "sqlite", Options: map[string]any{"url": ":memory:"}},
		Query:    inFlightWorkItems,
		Columns: []query.ColumnDef{
			{Name: "id"},
			{Name: "durationMs", Type: query.ColumnTypeNumber},
		},
		Order: query.Order{{Column: "durationMs", Desc: true}, {Column: "id", Unique: true}},
	}
}

// walkByCursor requests one page at a time, resuming from the cursor the last
// page handed back, which is what a table does between requests.
func walkByCursor(page query.PageRequest) [][]string {
	GinkgoHelper()
	var pages [][]string
	for {
		var served query.Page
		for got, err := range query.ExecutePages(context.New(), workItemsProfile(), page) {
			Expect(err).ToNot(HaveOccurred())
			served = got
			break
		}
		ids := make([]string, 0, len(served.Rows))
		for _, row := range served.Rows {
			ids = append(ids, fmt.Sprint(row["id"]))
		}
		pages = append(pages, ids)
		Expect(len(pages)).To(BeNumerically("<=", 7), "the walk never ends")
		if !served.HasMore {
			return pages
		}
		Expect(served.Next).ToNot(BeEmpty())
		page.Cursor = served.Next
	}
}

var _ = Describe("sqlite cursor paging over a nullable order column", func() {
	It("walks the declared descending order with nulls last", func() {
		Expect(walkByCursor(query.PageRequest{Limit: 2, Strategy: query.PagingCursor})).To(Equal([][]string{
			{"w3", "w7"}, {"w1", "w5"}, {"w2", "w4"}, {"w6"},
		}))
	})

	It("walks a requested ascending sort with nulls last", func() {
		Expect(walkByCursor(query.PageRequest{Limit: 2, Strategy: query.PagingCursor, Sort: "durationMs"})).To(Equal([][]string{
			{"w1", "w5"}, {"w7", "w3"}, {"w2", "w4"}, {"w6"},
		}))
	})
})
