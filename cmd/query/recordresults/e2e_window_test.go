package recordresults_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/cmd/query/recordresults"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// incident is a keyed, timed result type in the shape of a deadlock report: a
// stable id, the instant it happened and a database.
type incident struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
	DB string    `json:"db"`
}

const (
	incidentKind    = "incident"
	incidentProfile = "trace-results/incident"
	incidents       = 30
)

var incidentPath = "/api/v1/profile/" + url.PathEscape(incidentProfile)

// recentIncidents are incidents 1..incidents; incident n happened n hours
// before now less half an hour, so a whole-hour date-math edge never lands on
// one, and is appended as seq n.
func recentIncidents(now time.Time) []incident {
	items := make([]incident, 0, incidents)
	for n := 1; n <= incidents; n++ {
		items = append(items, incident{
			ID: fmt.Sprintf("incident-%02d", n), At: now.Add(-time.Duration(n)*time.Hour + 30*time.Minute).UTC(),
			DB: sampleDBs[n%3],
		})
	}
	return items
}

var _ = Describe("a keyed, timed record result type", Ordered, func() {
	var (
		ctx     = context.Background()
		source  recordstore.Backend
		service *profiles.Service
		server  resultServer
	)

	BeforeAll(func() {
		schemas := recordstore.NewSchemas()
		source = newKV(schemas)
		registry := newSampleRegistry(source, schemas)
		Expect(recordresults.RegisterResultType(registry, recordresults.ResultType[incident]{
			Kind: incidentKind, Title: "Incidents", TimeColumn: "at", KeyColumn: "id", DefaultFrom: "now-24h",
		})).To(Succeed())
		service = newResultService(registry)
		server = resultServer{handler: serveService(service)}
		result, err := recordstore.AppendTyped(ctx, source, "env-1", incidentKind, recentIncidents(time.Now()))
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(Equal(recordstore.AppendResult{Window: recordstore.Window{From: 1, To: incidents}}))
	})

	incidentRows := func(query string) ([]map[string]any, http.Header) {
		response := server.get(incidentPath+"?"+query, "application/json")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		var rows []map[string]any
		Expect(json.Unmarshal(response.Body.Bytes(), &rows)).To(Succeed(), response.Body.String())
		return rows, response.Header()
	}

	It("stores an overlapping re-sync once, so the stream's total does not move", func() {
		result, err := recordstore.AppendTyped(ctx, source, "env-1", incidentKind, recentIncidents(time.Now()))
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(Equal(recordstore.AppendResult{Window: recordstore.Window{From: incidents + 1, To: incidents}, Skipped: incidents}))

		_, header := incidentRows("stream=env-1&from=" + url.QueryEscape("now-7d"))
		Expect(header.Get("X-Total-Count")).To(Equal(fmt.Sprint(incidents)))
	})

	It("starts the window at the type's default from when the request names none", func() {
		rows, header := incidentRows("stream=env-1&limit=500")
		Expect(header.Get("X-Total-Count")).To(Equal("24"))
		Expect(column(rows, "id")[0]).To(Equal("incident-01"))
	})

	It("reads a date-math window newest first, a page at a time", func() {
		window := "stream=env-1&limit=4&from=" + url.QueryEscape("now-12h") + "&to=" + url.QueryEscape("now-6h")
		first, header := incidentRows(window)
		Expect(header.Get("X-Total-Count")).To(Equal("6"))
		Expect(column(first, "id")).To(Equal([]any{"incident-07", "incident-08", "incident-09", "incident-10"}))

		second, _ := incidentRows(window + "&cursor=" + url.QueryEscape(header.Get("X-Next-Cursor")))
		Expect(column(second, "id")).To(Equal([]any{"incident-11", "incident-12"}))
	})

	It("narrows a time window by the seq bounds", func() {
		rows, header := incidentRows("stream=env-1&afterSeq=8&toSeq=11&from=" + url.QueryEscape("now-12h"))
		Expect(header.Get("X-Total-Count")).To(Equal("3"))
		Expect(column(rows, "seq")).To(Equal([]any{float64(9), float64(10), float64(11)}))
	})

	It("reads the same rows through Run as over HTTP for the same filters and sort", func() {
		const filter, sort = "oipa,!audit", "db"
		httpRows, _ := incidentRows(fmt.Sprintf("stream=env-1&from=now-7d&filter.db=%s&sort=%s&order=desc&limit=500",
			url.QueryEscape(filter), sort))

		result, err := service.Run(ctx, incidentProfile, profiles.RunFlags{
			Params: []string{"stream=env-1", "from=now-7d"}, Filters: []string{"db=" + filter},
			Sort: sort, Order: "desc", Limit: 500,
		})
		Expect(err).ToNot(HaveOccurred())

		Expect(httpRows).ToNot(BeEmpty())
		Expect(column(httpRows, "db")).To(HaveEach(Equal("oipa")))
		Expect(runColumn(result.Rows, "id")).To(Equal(column(httpRows, "id")))
		Expect(runColumn(result.Rows, "seq")).To(Equal(column(httpRows, "seq")))
	})

	DescribeTable("refuses a Run filter or sort it could not apply",
		func(flags profiles.RunFlags, message string) {
			flags.Params = append(flags.Params, "stream=env-1")
			_, err := service.Run(ctx, incidentProfile, flags)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("a filter without a column", profiles.RunFlags{Filters: []string{"=oipa"}}, "--filter"),
		Entry("a column the profile does not filter", profiles.RunFlags{Filters: []string{"at=>now-1h"}}, `"filter.at" is not supported`),
		Entry("a column filtered twice", profiles.RunFlags{Filters: []string{"db=oipa", "db=audit"}}, "twice"),
		Entry("an order that is neither asc nor desc", profiles.RunFlags{Sort: "db", Order: "up"}, "asc or desc"),
		Entry("a direction with no sort column", profiles.RunFlags{Order: "desc"}, "no sort column"),
	)
})

// runColumn is one column of Run's rows as the JSON surface encodes it.
func runColumn(rows []query.Row, name string) []any {
	encoded := make([]map[string]any, len(rows))
	for index, row := range rows {
		encoded[index] = map[string]any{name: row[name]}
	}
	var decoded []map[string]any
	payload, err := json.Marshal(encoded)
	Expect(err).ToNot(HaveOccurred())
	Expect(json.Unmarshal(payload, &decoded)).To(Succeed())
	return column(decoded, name)
}
