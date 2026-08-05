package query_test

import (
	"iter"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// cappedProvider is a provider that silently applies a cap of its own — an
// index default, a configured limit — which is the shape that let a bounded
// read pass for a complete one.
type cappedProvider struct {
	typ      string
	rows     []query.Row
	cap      int
	modes    query.PagingMode
	lastPage query.PageRequest
}

func (c *cappedProvider) Type() string { return c.typ }

func (c *cappedProvider) PagingModes() query.PagingMode {
	if c.modes == 0 {
		return query.PagingOffset
	}
	return c.modes
}

func (c *cappedProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	rows, _ := c.serve()
	return rows, nil
}

func (c *cappedProvider) serve() ([]query.Row, bool) {
	if c.cap > 0 && len(c.rows) > c.cap {
		return c.rows[:c.cap], true
	}
	return c.rows, false
}

func (c *cappedProvider) Pages(ctx context.Context, req query.ProviderRequest, page query.PageRequest) iter.Seq2[query.Page, error] {
	c.lastPage = page
	return func(yield func(query.Page, error) bool) {
		served, capped := c.serve()
		for start := min(page.Offset, len(served)); ; start += page.Limit {
			end := min(start+page.Limit, len(served))
			more := end < len(served)
			if !yield(query.Page{Rows: served[start:end], HasMore: more, Truncated: capped && !more}, nil) {
				return
			}
			if !more {
				return
			}
		}
	}
}

func pagedProfile(name, providerType string) query.Profile {
	return query.Profile{
		Name:     name,
		Provider: query.ProviderConfig{Type: providerType},
		Order:    query.Order{{Column: "id", Unique: true}},
	}
}

var _ = Describe("ExecutePages", func() {
	ctx := context.New()

	It("yields the requested page and every page after it", func() {
		query.RegisterProvider(&cappedProvider{typ: "pages-walk", rows: rows(5)})

		var seen []query.Row
		for page, err := range query.ExecutePages(ctx, pagedProfile("walk", "pages-walk"), query.PageRequest{Limit: 2}) {
			Expect(err).ToNot(HaveOccurred())
			seen = append(seen, page.Rows...)
		}
		Expect(seen).To(Equal(rows(5)))
	})

	It("starts at the requested offset", func() {
		query.RegisterProvider(&cappedProvider{typ: "pages-offset", rows: rows(5)})

		var seen []query.Row
		for page, err := range query.ExecutePages(ctx, pagedProfile("offset", "pages-offset"), query.PageRequest{Limit: 10, Offset: 3}) {
			Expect(err).ToNot(HaveOccurred())
			seen = append(seen, page.Rows...)
		}
		Expect(seen).To(Equal(rows(5)[3:]))
	})

	// Without a tiebreaker two runs may interleave tied rows differently, so a
	// second page can repeat or skip rows from the first. Refusing is the only
	// answer that does not quietly produce a wrong page.
	It("refuses to page a profile that declares no total order", func() {
		query.RegisterProvider(&cappedProvider{typ: "pages-unordered", rows: rows(5)})
		profile := query.Profile{Name: "unordered", Provider: query.ProviderConfig{Type: "pages-unordered"}}

		_, err := query.CollectRows(query.Rows(query.ExecutePages(ctx, profile, query.PageRequest{Limit: 2, Offset: 2})))
		Expect(err).To(MatchError(ContainSubstring("no order is declared")))
	})

	It("still serves the first page of an unordered profile", func() {
		query.RegisterProvider(&cappedProvider{typ: "pages-unordered-first", rows: rows(5)})
		profile := query.Profile{Name: "unordered", Provider: query.ProviderConfig{Type: "pages-unordered-first"}}

		collected, err := query.CollectRows(query.Rows(query.ExecutePages(ctx, profile, query.PageRequest{Limit: 2})))
		Expect(err).ToNot(HaveOccurred())
		Expect(collected).To(Equal(rows(5)))
	})

	It("refuses a mode the provider does not serve", func() {
		query.RegisterProvider(&cappedProvider{typ: "pages-offset-only", rows: rows(5), modes: query.PagingOffset})

		_, err := query.CollectRows(query.Rows(query.ExecutePages(
			ctx, pagedProfile("offset-only", "pages-offset-only"), query.PageRequest{Limit: 2, Cursor: "abc"})))
		Expect(err).To(MatchError(ContainSubstring("cannot page by cursor")))
	})

	It("reports a provider without native paging as offset-capable", func() {
		query.RegisterProvider(&mockProvider{typ: "pages-buffered", rows: rows(5)})
		Expect(query.SupportsPaging("pages-buffered")).To(Equal(query.PagingOffset))
	})

	It("slices a buffered provider into pages with an exact total", func() {
		query.RegisterProvider(&mockProvider{typ: "pages-buffered-slice", rows: rows(5)})

		var totals []query.Total
		var seen []query.Row
		for page, err := range query.ExecutePages(ctx, pagedProfile("buffered", "pages-buffered-slice"), query.PageRequest{Limit: 2}) {
			Expect(err).ToNot(HaveOccurred())
			Expect(page.Total).ToNot(BeNil())
			totals = append(totals, *page.Total)
			seen = append(seen, page.Rows...)
		}
		Expect(seen).To(Equal(rows(5)))
		Expect(totals).To(HaveEach(query.Total{Value: 5, Exact: true}))
	})
})

var _ = Describe("Execute truncation", func() {
	ctx := context.New()

	// The P0 this contract exists for: a provider that quietly returned its own
	// default was indistinguishable from one that returned everything, because
	// truncation was only ever computed from the caller's bound.
	It("reports a cap the provider applied on an unbounded read", func() {
		query.RegisterProvider(&cappedProvider{typ: "capped-unbounded", rows: rows(50), cap: 10})

		result, err := query.Execute(ctx, pagedProfile("capped", "capped-unbounded"))
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(10))
		Expect(result.Truncated).To(BeTrue())
	})

	It("does not report truncation when the provider returned everything", func() {
		query.RegisterProvider(&cappedProvider{typ: "uncapped", rows: rows(50)})

		result, err := query.Execute(ctx, pagedProfile("uncapped", "uncapped"))
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(50))
		Expect(result.Truncated).To(BeFalse())
	})

	// The reconcile that used to read 500 documents per side and call itself
	// complete: the cap belonged to the backend, and nothing carried it out.
	It("reports a backend cap through a reconcile", func() {
		query.RegisterProvider(&cappedProvider{typ: "recon-capped", rows: rows(50), cap: 10})

		result, err := query.ReconcileProfiles(ctx, query.ReconcileRun{
			Source: pagedProfile("a", "recon-capped"),
			Dest:   pagedProfile("b", "recon-capped"),
			Config: query.ReconcileConfig{
				Dest:          "b",
				ReconcileSpec: query.ReconcileSpec{Key: query.KeySpec{Columns: []string{"id"}}},
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Bounded()).To(BeTrue())
	})

	It("does not report truncation when both sides were read in full", func() {
		query.RegisterProvider(&cappedProvider{typ: "recon-complete", rows: rows(10)})

		result, err := query.ReconcileProfiles(ctx, query.ReconcileRun{
			Source: pagedProfile("a", "recon-complete"),
			Dest:   pagedProfile("b", "recon-complete"),
			Config: query.ReconcileConfig{
				Dest:          "b",
				ReconcileSpec: query.ReconcileSpec{Key: query.KeySpec{Columns: []string{"id"}}},
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Bounded()).To(BeFalse())
		Expect(result.Stats.Matched).To(Equal(10))
	})
})
