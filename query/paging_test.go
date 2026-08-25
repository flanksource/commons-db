package query_test

import (
	"errors"
	"fmt"
	"iter"

	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// rows builds n identifiable rows, so an assertion can name which ones survived
// rather than only how many.
func rows(n int) []query.Row {
	built := make([]query.Row, 0, n)
	for i := range n {
		built = append(built, query.Row{"id": i})
	}
	return built
}

// pagesOf yields the given batches as Pages, which is what a provider does.
func pagesOf(batches ...[]query.Row) iter.Seq2[query.Page, error] {
	return func(yield func(query.Page, error) bool) {
		for index, batch := range batches {
			page := query.Page{Rows: batch, HasMore: index < len(batches)-1}
			if !yield(page, nil) {
				return
			}
		}
	}
}

func collect(seq iter.Seq2[query.Row, error]) ([]query.Row, error) {
	return query.CollectRows(seq)
}

var _ = Describe("PagingMode", func() {
	It("reports the modes a provider serves", func() {
		both := query.PagingOffset | query.PagingCursor
		Expect(both.Supports(query.PagingOffset)).To(BeTrue())
		Expect(both.Supports(query.PagingCursor)).To(BeTrue())
		Expect(query.PagingOffset.Supports(query.PagingCursor)).To(BeFalse())
		Expect(both.String()).To(Equal("offset,cursor"))
		Expect(query.PagingMode(0).String()).To(Equal("none"))
	})
})

var _ = Describe("PageRequest", func() {
	It("derives the mode from whether a cursor is carried", func() {
		Expect(query.PageRequest{Limit: 10}.Mode()).To(Equal(query.PagingOffset))
		Expect(query.PageRequest{Limit: 10, Cursor: "abc"}.Mode()).To(Equal(query.PagingCursor))
	})

	// The first page of a cursor walk carries no cursor yet, so without an
	// explicit strategy it would be indistinguishable from an offset page at
	// position zero — and would silently be served as one.
	It("takes the strategy from the request when no cursor says it", func() {
		Expect(query.PageRequest{Limit: 10, Strategy: query.PagingCursor}.Mode()).To(Equal(query.PagingCursor))
		Expect(query.PageRequest{Limit: 10, Strategy: query.PagingOffset}.Mode()).To(Equal(query.PagingOffset))
	})

	It("accepts a positioned page", func() {
		Expect(query.PageRequest{Limit: 10, Offset: 20}.Validate()).To(Succeed())
		Expect(query.PageRequest{Limit: 10, Cursor: "abc"}.Validate()).To(Succeed())
	})

	It("rejects a page that returns nothing", func() {
		Expect(query.PageRequest{Limit: 0}.Validate()).To(MatchError(ContainSubstring("greater than zero")))
		Expect(query.PageRequest{Limit: -1}.Validate()).To(MatchError(ContainSubstring("greater than zero")))
	})

	It("rejects a negative offset", func() {
		Expect(query.PageRequest{Limit: 10, Offset: -1}.Validate()).To(MatchError(ContainSubstring("zero or greater")))
	})

	// Two positions in one request can disagree, and no reading of that request
	// is obviously right, so it is refused rather than resolved.
	It("refuses a page positioned twice", func() {
		Expect(query.PageRequest{Limit: 10, Offset: 5, Cursor: "abc"}.Validate()).To(
			MatchError(ContainSubstring("cannot be combined with offset 5")))
		Expect(query.PageRequest{Limit: 10, Offset: 5, Strategy: query.PagingCursor}.Validate()).To(
			MatchError(ContainSubstring("cannot be combined with offset 5")))
	})

	It("refuses a request asking for more than one strategy", func() {
		err := query.PageRequest{Limit: 10, Strategy: query.PagingOffset | query.PagingCursor}.Validate()
		Expect(err).To(MatchError(ContainSubstring("one strategy")))
	})
})

var _ = Describe("Rows", func() {
	It("flattens every page in order", func() {
		flattened, err := collect(query.Rows(pagesOf(rows(2), rows(3))))
		Expect(err).ToNot(HaveOccurred())
		Expect(flattened).To(Equal(append(rows(2), rows(3)...)))
	})

	It("surfaces a page error and stops", func() {
		boom := errors.New("backend went away")
		seq := func(yield func(query.Page, error) bool) {
			if !yield(query.Page{Rows: rows(1)}, nil) {
				return
			}
			yield(query.Page{}, boom)
		}
		_, err := collect(query.Rows(seq))
		Expect(err).To(MatchError(boom))
	})

	// Ending the range is what releases a backend cursor, so a consumer that
	// stops early must actually stop the producer rather than leave it running.
	It("stops the underlying pages when the consumer stops", func() {
		produced := 0
		seq := func(yield func(query.Page, error) bool) {
			for batch := range 10 {
				produced++
				if !yield(query.Page{Rows: rows(1), HasMore: batch < 9}, nil) {
					return
				}
			}
		}
		for range query.Rows(seq) {
			break
		}
		Expect(produced).To(Equal(1))
	})
})

var _ = Describe("Limit", func() {
	It("stops after n rows", func() {
		limited, err := collect(query.Limit(query.Rows(pagesOf(rows(5))), 3))
		Expect(err).ToNot(HaveOccurred())
		Expect(limited).To(Equal(rows(3)))
	})

	It("passes a shorter sequence through untouched", func() {
		limited, err := collect(query.Limit(query.Rows(pagesOf(rows(2))), 3))
		Expect(err).ToNot(HaveOccurred())
		Expect(limited).To(Equal(rows(2)))
	})

	It("enforces the ceiling across page boundaries", func() {
		limited, err := collect(query.Limit(query.Rows(pagesOf(rows(2), rows(2), rows(2))), 5))
		Expect(err).ToNot(HaveOccurred())
		Expect(limited).To(HaveLen(5))
	})

	It("surfaces an error rather than truncating at it", func() {
		boom := errors.New("cursor expired")
		seq := func(yield func(query.Row, error) bool) {
			if !yield(query.Row{"id": 0}, nil) {
				return
			}
			yield(nil, boom)
		}
		_, err := collect(query.Limit(seq, 10))
		Expect(err).To(MatchError(boom))
	})

	// A zero ceiling would silently yield nothing, which reads identically to an
	// empty result set.
	It("refuses a ceiling that could return nothing", func() {
		Expect(func() { query.Limit(query.Rows(pagesOf(rows(1))), 0) }).To(Panic())
	})
})

var _ = Describe("SlicePages", func() {
	It("yields one page holding the whole result", func() {
		var pages []query.Page
		for page, err := range query.SlicePages(rows(3)) {
			Expect(err).ToNot(HaveOccurred())
			pages = append(pages, page)
		}
		Expect(pages).To(HaveLen(1))
		Expect(pages[0].Rows).To(Equal(rows(3)))
		Expect(pages[0].HasMore).To(BeFalse())
		Expect(pages[0].Next).To(BeEmpty())
	})

	// The whole result is in hand, so the count is knowable exactly — which is
	// the one case where a total may be reported without a caveat.
	It("reports an exact total", func() {
		for page, err := range query.SlicePages(rows(3)) {
			Expect(err).ToNot(HaveOccurred())
			Expect(page.Total).ToNot(BeNil())
			Expect(*page.Total).To(Equal(query.Total{Value: 3, Exact: true}))
		}
	})
})

var _ = Describe("ErrorPage", func() {
	It("fails a provider out of setup without a second return value", func() {
		boom := fmt.Errorf("no connection")
		_, err := collect(query.Rows(query.ErrorPage(boom)))
		Expect(err).To(MatchError(boom))
	})
})
