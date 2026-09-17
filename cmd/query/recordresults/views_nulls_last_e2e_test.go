package recordresults_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/recordresults"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// workEvent starts a work item and, once it is done, ends it with a duration.
type workEvent struct {
	Item   string  `json:"item"`
	Phase  string  `json:"phase"`
	Millis float64 `json:"millis"`
}

// workItemViews groups each item's events into one row whose duration stays
// null while the item is in flight.
func registerWorkEvents(registry *recordresults.Registry) error {
	return recordresults.RegisterResultType(registry, recordresults.ResultType[workEvent]{
		Kind: "work_event", Title: "Work events",
		Views: []recordresults.ResultView{{
			Name: "work_items", Title: "Work items",
			Query: `SELECT item, max(CASE WHEN phase = 'end' THEN millis END) AS durationMs
FROM stream_rows GROUP BY item`,
			Columns: []query.ColumnDef{{Name: "item"}, numberColumn("durationMs")},
			Order:   query.Order{{Column: "durationMs", Desc: true}, {Column: "item", Unique: true}},
		}},
	})
}

// Items a, b, c and g end in 30, 50, 20 and 10ms; d, e and f are still in
// flight, so pages of two end between the last duration and the first null,
// and inside the nulls.
func inFlightWorkEvents() []workEvent {
	events := []workEvent{}
	for _, item := range []string{"g", "a", "b", "c", "d", "e", "f"} {
		events = append(events, workEvent{Item: item, Phase: "start"})
	}
	for item, millis := range map[string]float64{"g": 10, "a": 30, "b": 50, "c": 20} {
		events = append(events, workEvent{Item: item, Phase: "end", Millis: millis})
	}
	return events
}

var _ = Describe("a view paged by a nullable output column", Ordered, func() {
	var server followServer

	BeforeAll(func() {
		schemas := recordstore.NewSchemas()
		server = newFollowServerWith(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: localSettings(""), Source: kvRouter(schemas),
			Schemas: schemas, Register: registerWorkEvents,
		})
		_, err := recordstore.AppendTyped(forTenant("a"), server.results.Backend, "run-1", "work_event", inFlightWorkEvents())
		Expect(err).ToNot(HaveOccurred())
	})

	walk := func(request string) [][]any {
		GinkgoHelper()
		var pages [][]any
		cursor := ""
		for {
			target := viewPath("work_event", "work_items") + "?stream=run-1&limit=2&" + request
			if cursor != "" {
				target += "&cursor=" + url.QueryEscape(cursor)
			}
			response := server.do(http.MethodGet, target, http.Header{"Accept": {"application/json"}, tenantHeader: {"a"}})
			var body bytes.Buffer
			_, err := body.ReadFrom(response.Body)
			Expect(err).ToNot(HaveOccurred())
			Expect(response.StatusCode).To(Equal(http.StatusOK), body.String())
			var rows []map[string]any
			Expect(json.Unmarshal(body.Bytes(), &rows)).To(Succeed(), body.String())
			pages = append(pages, column(rows, "item"))
			Expect(len(pages)).To(BeNumerically("<=", 7), "the walk never ends")
			if response.Header.Get("X-Has-More") != "true" {
				return pages
			}
			cursor = response.Header.Get("X-Next-Cursor")
			Expect(cursor).ToNot(BeEmpty())
		}
	}

	It("walks the declared slowest-first order with in-flight items last", func() {
		Expect(walk("")).To(Equal([][]any{{"b", "a"}, {"c", "g"}, {"d", "e"}, {"f"}}))
	})

	It("walks a requested fastest-first sort with in-flight items still last", func() {
		Expect(walk("sort=durationMs&order=asc")).To(Equal([][]any{{"g", "c"}, {"a", "b"}, {"d", "e"}, {"f"}}))
	})
})
