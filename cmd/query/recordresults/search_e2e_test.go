package recordresults_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/recordresults"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

const searchedKind = "searched_event"

var searchedPath = "/api/v1/profile/" + url.PathEscape("trace-results/"+searchedKind)

// searchedSeqs are the seqs of run-1's events, newest first, that keep passes:
// event n is appended as seq n and captured n milliseconds after the start.
func searchedSeqs(first, last int, keep func(sampleEvent) bool) []any {
	seqs := []any{}
	events := sampleEvents(1, 250)
	for index := len(events) - 1; index >= 0; index-- {
		n := index + 1
		if n >= first && n <= last && keep(events[index]) {
			seqs = append(seqs, float64(n))
		}
	}
	return seqs
}

// contains is the search's own semantics, written independently of SQL: a
// case-insensitive substring of any search column.
func contains(needle string, event sampleEvent) bool {
	needle = strings.ToLower(needle)
	haystacks := append([]string{event.DB, event.User}, event.Tables...)
	for _, haystack := range haystacks {
		if strings.Contains(strings.ToLower(haystack), needle) {
			return true
		}
	}
	return false
}

var _ = Describe("searching a record result type", Ordered, func() {
	var server resultServer

	BeforeAll(func() {
		ctx := context.Background()
		schemas := recordstore.NewSchemas()
		source := newKV(schemas)
		registry := newSampleRegistry(source, schemas)
		Expect(recordresults.RegisterResultType(registry, recordresults.ResultType[sampleEvent]{
			Kind: searchedKind, Title: "Searched events", TimeColumn: "at", SearchColumns: []string{"db", "user", "tables"},
		})).To(Succeed())
		_, err := recordstore.AppendTyped(ctx, source, "run-1", searchedKind, sampleEvents(1, 250))
		Expect(err).ToNot(HaveOccurred())
		server = resultServer{source: source, registry: registry, handler: serveResults(registry)}
	})

	get := func(query string) ([]any, http.Header) {
		response := server.get(searchedPath+"?"+query, "application/json")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		var rows []map[string]any
		Expect(json.Unmarshal(response.Body.Bytes(), &rows)).To(Succeed(), response.Body.String())
		return column(rows, "seq"), response.Header()
	}

	It("declares its search as a search-role param over the type's search columns", func() {
		profile, err := server.registry.Get(context.Background(), "trace-results/"+searchedKind)
		Expect(err).ToNot(HaveOccurred())
		Expect(profile.Params).To(ContainElement(query.ParamDef{
			Name: "q", Label: "Search", Role: query.ParamRoleSearch, Default: "",
			Description: "Keep the rows where db, user or tables contains this text, ignoring case",
		}))
	})

	DescribeTable("keeps the rows any search column contains the text in, ignoring case",
		func(needle string) {
			expected := searchedSeqs(1, 250, func(event sampleEvent) bool { return contains(needle, event) })
			seqs, header := get("stream=run-1&limit=500&q=" + url.QueryEscape(needle))
			Expect(header.Get("X-Total-Count")).To(Equal(fmt.Sprint(len(expected))))
			Expect(seqs).To(Equal(expected))
		},
		Entry("a scalar column, in another case", "AUD"),
		Entry("a list column's element", "activ"),
		Entry("either of two columns", "o"),
		Entry("a character SQL LIKE would read as a wildcard", "%"),
	)

	It("reads every row when the search is empty", func() {
		_, header := get("stream=run-1&limit=1&q=")
		Expect(header.Get("X-Total-Count")).To(Equal("250"))
	})

	Context("combined with the seq window, the time window and a column filter", func() {
		const window = "stream=run-1&afterSeq=12&toSeq=200&from=2026-09-10T06:00:00.020Z&q=ALI&filter.db=oipa"
		keep := func(event sampleEvent) bool { return event.DB == "oipa" && contains("ali", event) }
		expected := func() []any { return searchedSeqs(20, 200, keep) }

		It("applies all of them to a page and its total", func() {
			seqs, header := get(window + "&limit=10")
			Expect(header.Get("X-Total-Count")).To(Equal(fmt.Sprint(len(expected()))))
			Expect(seqs).To(Equal(expected()[:10]))
		})

		It("keeps all of them in a page export resumed from a cursor", func() {
			_, header := get(window + "&limit=10")
			response := server.get(searchedPath+"?"+window+"&limit=10&format=json&cursor="+url.QueryEscape(header.Get("X-Next-Cursor")), "")
			Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
			var rows []map[string]any
			Expect(json.Unmarshal(response.Body.Bytes(), &rows)).To(Succeed(), response.Body.String())
			Expect(column(rows, "seq")).To(Equal(expected()[10:20]))
		})

		It("keeps all of them in an all-row export", func() {
			response := server.get(searchedPath+"?"+window+"&scope=all&format=ndjson", "")
			Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
			var seqs []any
			scanner := bufio.NewScanner(bytes.NewReader(response.Body.Bytes()))
			for scanner.Scan() {
				var row map[string]any
				Expect(json.Unmarshal(scanner.Bytes(), &row)).To(Succeed(), scanner.Text())
				seqs = append(seqs, row["seq"])
			}
			Expect(seqs).To(Equal(expected()))
		})
	})
})

var _ = Describe("declaring a result type's search columns", func() {
	DescribeTable("refuses a search it could not run",
		func(columns []string, message string) {
			registry, _ := newRegistry()
			err := recordresults.RegisterResultType(registry, recordresults.ResultType[sampleEvent]{
				Kind: "sample_event", Title: "Sample events", SearchColumns: columns,
			})
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("a column the type does not have", []string{"db", "missing"}, `search column "missing" is not one of its columns`),
		Entry("a column that holds no text", []string{"slow"}, `search column "slow" is boolean`),
		Entry("a column named twice", []string{"db", "db"}, `search column "db" is named twice`),
	)
})
