package providers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// openSearchStub answers searches and point-in-time calls, recording what was
// asked of it so a test can assert the mechanism rather than only the rows.
type openSearchStub struct {
	server *httptest.Server

	sizes      []string
	bodies     []map[string]any
	openedPITs int
	closedPITs int
	indexed    []string
}

func newOpenSearchStub(hits func(call int) string) *openSearchStub {
	stub := &openSearchStub{}
	calls := 0
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.Contains(r.URL.Path, "/_search/point_in_time"):
			if r.Method == http.MethodDelete {
				stub.closedPITs++
				_, _ = fmt.Fprint(w, `{"pits":[{"successful":true,"pit_id":"pit-1"}]}`)
				return
			}
			stub.openedPITs++
			_, _ = fmt.Fprint(w, `{"pit_id":"pit-1","creation_time":1}`)
			return
		default:
			stub.sizes = append(stub.sizes, r.URL.Query().Get("size"))
			stub.indexed = append(stub.indexed, r.URL.Path)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			stub.bodies = append(stub.bodies, body)
			response := hits(calls)
			calls++
			_, _ = fmt.Fprint(w, response)
		}
	}))
	return stub
}

func (s *openSearchStub) profile(name string, order query.Order) query.Profile {
	return query.Profile{
		Name: name,
		Provider: query.ProviderConfig{
			Type:    "opensearch",
			Options: map[string]any{"address": s.server.URL, "index": "logs"},
		},
		Query: `{"query":{"match_all":{}}}`,
		Order: order,
	}
}

var _ = Describe("opensearch provider", func() {
	hit := func(id string, sort ...any) string {
		encoded, err := json.Marshal(sort)
		Expect(err).ToNot(HaveOccurred())
		return fmt.Sprintf(`{"_index":"logs","_id":%q,"_source":{"message":%q},"sort":%s}`, id, id, encoded)
	}
	response := func(total int, relation string, hits ...string) string {
		return fmt.Sprintf(`{"took":1,"timed_out":false,"hits":{"total":{"value":%d,"relation":%q},"hits":[%s]}}`,
			total, relation, strings.Join(hits, ","))
	}

	It("asks for exactly the page it was given, without a scroll", func() {
		stub := newOpenSearchStub(func(int) string { return response(1, "eq", hit("one")) })
		defer stub.server.Close()

		var pages []query.Page
		for page, err := range query.ExecutePages(context.New(), stub.profile("bounded", nil), query.PageRequest{Limit: 101}) {
			Expect(err).ToNot(HaveOccurred())
			pages = append(pages, page)
		}
		Expect(pages).To(HaveLen(1))
		Expect(pages[0].Rows).To(HaveLen(1))
		Expect(pages[0].Rows[0]).To(HaveKeyWithValue("message", "one"))
		Expect(stub.sizes).To(Equal([]string{"101"}))
		Expect(stub.openedPITs).To(BeZero())
	})

	// The total is what the index could state. Past track_total_hits it states a
	// lower bound, and a caller shown that as a total would read "10000" where
	// the truth is "at least 10000".
	It("carries whether the index could state the total exactly", func() {
		stub := newOpenSearchStub(func(int) string { return response(10000, "gte", hit("one")) })
		defer stub.server.Close()

		for page, err := range query.ExecutePages(context.New(), stub.profile("inexact", nil), query.PageRequest{Limit: 10}) {
			Expect(err).ToNot(HaveOccurred())
			Expect(page.Total).ToNot(BeNil())
			Expect(*page.Total).To(Equal(query.Total{Value: 10000, Exact: false}))
		}
	})

	// The P0 this contract was built to remove: an index read with no explicit
	// bound used to stop at a 500-row backend default and say nothing, so a
	// large index was indistinguishable from a small one. A read of everything
	// now walks the pages and returns all of them.
	It("reads past the backend default a bare search used to stop at", func() {
		const total = 2500
		stub := newOpenSearchStub(func(call int) string {
			start := call * query.DefaultMaxPageSize
			end := min(start+query.DefaultMaxPageSize, total)
			hits := make([]string, 0, end-start)
			for i := start; i < end; i++ {
				hits = append(hits, hit(fmt.Sprintf("doc-%d", i), i))
			}
			return response(total, "eq", hits...)
		})
		defer stub.server.Close()

		result, err := query.Execute(context.New(), stub.profile("unbounded", query.Order{{Column: "seq", Unique: true}}))
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(total))
		Expect(result.Truncated).To(BeFalse())
	})

	// Reading past the result window used to switch the whole read to a scroll,
	// changing both its cost and its consistency at a boundary no caller could
	// see. It is now a refusal that names the fix.
	It("refuses an offset past the index result window instead of changing mechanism", func() {
		stub := newOpenSearchStub(func(int) string { return response(50000, "eq", hit("one", 1, "one")) })
		defer stub.server.Close()

		order := query.Order{{Column: "@timestamp"}, {Column: "_id", Unique: true}}
		_, err := query.CollectRows(query.Rows(query.ExecutePages(
			context.New(), stub.profile("deep", order), query.PageRequest{Limit: 100, Offset: 20000})))
		Expect(err).To(MatchError(ContainSubstring("needs a cursor")))
		Expect(err).To(MatchError(ContainSubstring("result window")))
	})

	// A sample is a look at the shape of the data, so it asks for one page of
	// that size. It used to drain the whole index and cut the first hundred rows
	// out of the result, which on any index larger than the result window did not
	// return a sample at all — it returned "reading past row 10000 needs a
	// cursor".
	It("samples one page instead of draining the index", func() {
		stub := newOpenSearchStub(func(call int) string {
			hits := make([]string, 0, query.DefaultMaxPageSize)
			for i := 0; i < query.DefaultMaxPageSize; i++ {
				hits = append(hits, hit(fmt.Sprintf("doc-%d", call*query.DefaultMaxPageSize+i)))
			}
			return response(50000, "eq", hits...)
		})
		defer stub.server.Close()

		result, err := query.Sample(context.New(), stub.profile("sampled", nil), query.SampleOptions{
			Page: query.PageRequest{Limit: 10},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(10))
		Expect(result.Truncated).To(BeTrue())
		Expect(stub.sizes).To(Equal([]string{"10"}))
	})

	// A read of everything is not a caller asking for a deep offset: it is the
	// provider choosing how to walk, and an ordered index is walked by the one
	// strategy that reaches the end of it.
	It("drains an ordered index by cursor so a read of everything passes the result window", func() {
		// The batch a drain asks for is the provider's, and the stub has to serve
		// full batches for a short one to mean the end of the index — so the size
		// on the wire is asserted rather than assumed.
		const batch = query.DefaultMaxPageSize
		const total = 12000
		stub := newOpenSearchStub(func(call int) string {
			start := call * batch
			end := min(start+batch, total)
			hits := make([]string, 0, end-start)
			for i := start; i < end; i++ {
				hits = append(hits, hit(fmt.Sprintf("doc-%d", i), i))
			}
			return response(total, "eq", hits...)
		})
		defer stub.server.Close()

		provider, err := query.GetProvider("opensearch")
		Expect(err).ToNot(HaveOccurred())
		rows, err := provider.Execute(context.New(), query.ProviderRequest{
			Query:   `{"query":{"match_all":{}}}`,
			Options: map[string]any{"address": stub.server.URL, "index": "logs"},
			Order:   query.Order{{Column: "seq", Unique: true}},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(total))
		Expect(stub.sizes).To(HaveEach(strconv.Itoa(batch)))
		Expect(stub.openedPITs).To(Equal(1))
		Expect(stub.closedPITs).To(Equal(1))
	})

	// Offset and cursor are both useful on the same profile: offset can jump to
	// an arbitrary page and cursor cannot, cursor reads past the window and
	// offset cannot. An offset page therefore still hands back a cursor, so a
	// caller can start with page numbers and carry on by following it.
	It("hands back a cursor from an offset page so a caller can switch at depth", func() {
		stub := newOpenSearchStub(func(int) string {
			return response(50000, "eq", hit("one", 1, "one"), hit("two", 2, "two"))
		})
		defer stub.server.Close()

		order := query.Order{{Column: "@timestamp"}, {Column: "_id", Unique: true}}
		for page, err := range query.ExecutePages(context.New(), stub.profile("hybrid", order), query.PageRequest{Limit: 2, Offset: 4}) {
			Expect(err).ToNot(HaveOccurred())
			Expect(page.Next).ToNot(BeEmpty())
			Expect(stub.openedPITs).To(BeZero())
			Expect(stub.bodies[0]["from"]).To(Equal(float64(4)))
			break
		}
	})

	Describe("cursor paging", func() {
		order := query.Order{{Column: "@timestamp"}, {Column: "_id", Unique: true}}

		It("walks by search_after over a point-in-time it opens and closes", func() {
			stub := newOpenSearchStub(func(call int) string {
				if call == 0 {
					return response(3, "eq", hit("one", 1, "one"), hit("two", 2, "two"))
				}
				return response(3, "eq", hit("three", 3, "three"))
			})
			defer stub.server.Close()

			var seen []query.Row
			for page, err := range query.ExecutePages(context.New(), stub.profile("cursored", order), query.PageRequest{Limit: 2, Strategy: query.PagingCursor}) {
				Expect(err).ToNot(HaveOccurred())
				seen = append(seen, page.Rows...)
			}
			Expect(seen).To(HaveLen(3))
			Expect(stub.openedPITs).To(Equal(1))
			Expect(stub.closedPITs).To(Equal(1))

			// The second search resumes after the first page's last sort values.
			Expect(stub.bodies).To(HaveLen(2))
			Expect(stub.bodies[0]).ToNot(HaveKey("search_after"))
			Expect(stub.bodies[1]["search_after"]).To(Equal([]any{float64(2), "two"}))
		})

		It("hands back a cursor that resumes the same point-in-time", func() {
			stub := newOpenSearchStub(func(call int) string {
				if call == 0 {
					return response(3, "eq", hit("one", 1, "one"), hit("two", 2, "two"))
				}
				return response(3, "eq", hit("three", 3, "three"))
			})
			defer stub.server.Close()

			var next query.Cursor
			for page, err := range query.ExecutePages(context.New(), stub.profile("cursored", order), query.PageRequest{Limit: 2, Strategy: query.PagingCursor}) {
				Expect(err).ToNot(HaveOccurred())
				next = page.Next
				break
			}
			Expect(next).ToNot(BeEmpty())
			Expect(stub.closedPITs).To(BeZero())

			for page, err := range query.ExecutePages(context.New(), stub.profile("cursored", order), query.PageRequest{Limit: 2, Cursor: next}) {
				Expect(err).ToNot(HaveOccurred())
				Expect(page.Rows).To(HaveLen(1))
				Expect(page.Rows[0]).To(HaveKeyWithValue("message", "three"))
				Expect(page.Next).To(BeEmpty())
			}
			Expect(stub.bodies).To(HaveLen(2))
			Expect(stub.bodies[1]["search_after"]).To(Equal([]any{float64(2), "two"}))
			Expect(stub.bodies[1]["pit"]).To(Equal(map[string]any{"id": "pit-1", "keep_alive": "60s"}))
			Expect(stub.closedPITs).To(Equal(1))
		})

		// A page carrying a cursor must leave its point-in-time alive, or the
		// cursor it just returned cannot be resumed. An abandoned cursor expires
		// under the PIT keepalive.
		It("keeps the point-in-time alive while a returned cursor can resume it", func() {
			stub := newOpenSearchStub(func(int) string {
				return response(9, "eq", hit("one", 1, "one"), hit("two", 2, "two"))
			})
			defer stub.server.Close()

			for _, err := range query.ExecutePages(context.New(), stub.profile("cursored", order), query.PageRequest{Limit: 2, Strategy: query.PagingCursor}) {
				Expect(err).ToNot(HaveOccurred())
				break
			}
			Expect(stub.closedPITs).To(BeZero())
		})

		It("refuses a cursor on a profile that declares no order", func() {
			stub := newOpenSearchStub(func(int) string { return response(1, "eq", hit("one")) })
			defer stub.server.Close()

			_, err := query.CollectRows(query.Rows(query.ExecutePages(
				context.New(), stub.profile("unordered", nil), query.PageRequest{Limit: 2, Cursor: "abc"})))
			Expect(err).To(MatchError(ContainSubstring("no order is declared")))
		})
	})
})

func jsonServer(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

var _ = Describe("http provider", func() {
	It("uses an inline base URL from provider options", func() {
		srv := jsonServer(http.StatusOK, `[{"source":"options"}]`)
		defer srv.Close()

		result, err := query.Execute(context.New(), query.Profile{
			Name: "http-options-url",
			Provider: query.ProviderConfig{
				Type:    "http",
				Options: map[string]any{"url": srv.URL},
			},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(ConsistOf(query.Row{"source": "options"}))
	})

	It("returns one row per element of a JSON array response", func() {
		srv := jsonServer(http.StatusOK, `[{"id":1,"name":"a"},{"id":2,"name":"b"}]`)
		defer srv.Close()

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "http-array",
			Provider: query.ProviderConfig{Type: "http"},
			Query:    srv.URL,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(2))
		Expect(result.Rows[0]).To(HaveKeyWithValue("name", "a"))
		Expect(result.Rows[1]).To(HaveKeyWithValue("name", "b"))
	})

	It("extracts an inner array via jsonpath", func() {
		srv := jsonServer(http.StatusOK, `{"Traces":[{"x":1},{"x":2},{"x":3}],"total":3}`)
		defer srv.Close()

		result, err := query.Execute(context.New(), query.Profile{
			Name: "http-jsonpath",
			Provider: query.ProviderConfig{
				Type:    "http",
				Options: map[string]any{"jsonpath": "$.Traces"},
			},
			Query: srv.URL,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(3))
	})

	It("fails loudly on a non-2xx response", func() {
		srv := jsonServer(http.StatusInternalServerError, `{"error":"boom"}`)
		defer srv.Close()

		_, err := query.Execute(context.New(), query.Profile{
			Name:     "http-error",
			Provider: query.ProviderConfig{Type: "http"},
			Query:    srv.URL,
		})
		Expect(err).To(MatchError(ContainSubstring("status 500")))
	})
})

var _ = Describe("prometheus provider", func() {
	const vectorResponse = `{"status":"success","data":{"resultType":"vector","result":[` +
		`{"metric":{"__name__":"up","instance":"a"},"value":[1700000000,"1"]},` +
		`{"metric":{"__name__":"up","instance":"b"},"value":[1700000000,"0"]}]}}`

	It("returns one row per vector sample with the value", func() {
		srv := jsonServer(http.StatusOK, vectorResponse)
		defer srv.Close()

		result, err := query.Execute(context.New(), query.Profile{
			Name: "prom",
			Provider: query.ProviderConfig{
				Type:    "prometheus",
				Options: map[string]any{"url": srv.URL},
			},
			Query: "up",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(2))
		Expect(result.Rows[0]).To(HaveKeyWithValue("value", float64(1)))
		Expect(result.Rows[0]).To(HaveKeyWithValue("instance", "a"))
	})

	It("restricts labels via selectLabels", func() {
		srv := jsonServer(http.StatusOK, vectorResponse)
		defer srv.Close()

		result, err := query.Execute(context.New(), query.Profile{
			Name: "prom-select",
			Provider: query.ProviderConfig{
				Type:    "prometheus",
				Options: map[string]any{"url": srv.URL, "selectLabels": []string{"instance"}},
			},
			Query: "up",
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows[0]).To(HaveKey("instance"))
		Expect(result.Rows[0]).ToNot(HaveKey("__name__"))
	})
})
