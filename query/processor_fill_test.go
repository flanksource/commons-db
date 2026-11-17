package query_test

import (
	"fmt"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	_ "github.com/flanksource/commons-db/query/processor"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A processor that drops rows leaves a page short of what was asked for. The
// page is filled from the source's next pages instead, so a reader gets a full
// page and no processor has to remember earlier pages inside the cursor.
var _ = Describe("ProcessPages filling a page", func() {
	dedupe := []query.ProcessorSpec{{Type: "cel.dedupe", Config: map[string]any{"partition": []any{"id"}}}}
	const pageSize = 4

	raw := func(position int, more bool, ids ...string) query.Page {
		rows := make([]query.Row, 0, len(ids))
		for _, id := range ids {
			rows = append(rows, query.Row{"id": id})
		}
		page := query.Page{Rows: rows, HasMore: more}
		if more {
			page.NextKeys, page.NextOffset = []any{position}, position
		}
		return page
	}
	type served struct {
		IDs        []string
		HasMore    bool
		NextOffset int
		State      map[string][]byte
	}
	process := func(limit int, source ...query.Page) ([]served, int) {
		pulled := 0
		pages := func(yield func(query.Page, error) bool) {
			for _, page := range source {
				pulled++
				if !yield(page, nil) {
					return
				}
			}
		}
		var out []served
		for page, err := range query.ProcessPages(context.Context{}, query.ProcessPagesRequest{Specs: dedupe, Limit: limit}, pages) {
			Expect(err).ToNot(HaveOccurred())
			ids := make([]string, 0, len(page.Rows))
			for _, row := range page.Rows {
				ids = append(ids, fmt.Sprint(row["id"]))
			}
			out = append(out, served{IDs: ids, HasMore: page.HasMore, NextOffset: page.NextOffset, State: page.State})
		}
		return out, pulled
	}

	It("keeps reading the source until the deduplicated page holds the page size, resuming after the last page it read", func() {
		pages, _ := process(pageSize,
			raw(3, true, "a", "a", "b"),
			raw(6, true, "b", "c", "c"),
			raw(8, true, "d", "e"),
			raw(0, false, "f"),
		)
		Expect(pages).To(Equal([]served{
			// a and b repeat across the first two source pages: one page, deduped together.
			{IDs: []string{"a", "b", "c", "d", "e"}, HasMore: true, NextOffset: 8},
			{IDs: []string{"f"}, HasMore: false},
		}))
	})

	It("serves a short page at the end of the source rather than waiting for rows that do not exist", func() {
		pages, pulled := process(pageSize, raw(0, false, "a", "a"))
		Expect(pages).To(Equal([]served{{IDs: []string{"a"}}}))
		Expect(pulled).To(Equal(1))
	})

	It("passes each source page through as it is when no page size is asked for", func() {
		pages, _ := process(0, raw(2, true, "a", "a"), raw(0, false, "b"))
		Expect(pages).To(Equal([]served{
			{IDs: []string{"a"}, HasMore: true, NextOffset: 2},
			{IDs: []string{"b"}},
		}))
	})
})
