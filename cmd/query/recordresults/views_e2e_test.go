package recordresults_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/recordresults"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// jobEvent is a result type whose rows a view groups: each job starts and,
// unless it is still running, ends with a status and a duration.
type jobEvent struct {
	ID     string  `json:"id"`
	Parent string  `json:"parent"`
	Job    string  `json:"job"`
	Phase  string  `json:"phase"`
	Status string  `json:"status"`
	Millis float64 `json:"millis"`
}

// auditEvent is a second kind, with a view of the same name as one of
// jobEvent's, so a view name is shown to be scoped to its kind.
type auditEvent struct {
	Job string `json:"job"`
}

func numberColumn(name string) query.ColumnDef {
	return query.ColumnDef{Name: name, Type: query.ColumnTypeNumber}
}

// jobViews declares overview before the views it uses, so resolving uses
// cannot lean on declaration order.
func jobViews() []recordresults.ResultView {
	return []recordresults.ResultView{
		{
			Name: "overview", Title: "Overview", Uses: []string{"busy_jobs"},
			Params: []query.ParamDef{{Name: "minMs", Label: "Slow from", Type: query.ParamTypeNumber, Required: true}},
			Query: `WITH slow AS (SELECT job FROM busy_jobs WHERE totalMs >= {{.params.minMs}})
SELECT 'all' AS scope, (SELECT count(*) FROM jobs) AS jobs, (SELECT count(*) FROM busy_jobs) AS busy, (SELECT count(*) FROM slow) AS slow`,
			Columns: []query.ColumnDef{{Name: "scope"}, numberColumn("jobs"), numberColumn("busy"), numberColumn("slow")},
			Order:   query.Order{{Column: "scope", Unique: true}},
		},
		{
			Name: "jobs", Title: "Jobs",
			Query: `-- one row per job, whichever of its events the window holds
WITH events AS (SELECT job, phase, millis, seq FROM stream_rows)
SELECT job, count(*) AS events, sum(phase = 'end') AS ended, total(millis) AS totalMs, max(seq) AS lastSeq
FROM events GROUP BY job`,
			Columns: []query.ColumnDef{{Name: "job"}, numberColumn("events"), numberColumn("ended"), numberColumn("totalMs"), numberColumn("lastSeq")},
			Order:   query.Order{{Column: "totalMs", Desc: true}, {Column: "job", Unique: true}},
		},
		{
			Name: "busy_jobs", Title: "Busy jobs", Uses: []string{"jobs"},
			Query:   `SELECT job, totalMs FROM jobs WHERE ended > 0`,
			Columns: []query.ColumnDef{{Name: "job"}, numberColumn("totalMs")},
			Order:   query.Order{{Column: "job", Unique: true}},
		},
	}
}

func registerJobTypes(views []recordresults.ResultView) func(*recordresults.Registry) error {
	return func(registry *recordresults.Registry) error {
		if err := recordresults.RegisterResultType(registry, recordresults.ResultType[jobEvent]{
			Kind: "job_event", Title: "Job events", KeyColumn: "id", Follow: true, SearchColumns: []string{"job"},
			Hierarchy: &recordresults.HierarchyColumns{ID: "id", Parent: "parent"}, Views: views,
		}); err != nil {
			return err
		}
		return recordresults.RegisterResultType(registry, recordresults.ResultType[auditEvent]{
			Kind: "audit_event", Title: "Audit events",
			Views: []recordresults.ResultView{{
				Name: "jobs", Title: "Audited jobs", Query: `SELECT job, count(*) AS audits FROM stream_rows GROUP BY job`,
				Columns: []query.ColumnDef{{Name: "job"}, numberColumn("audits")},
				Order:   query.Order{{Column: "job", Unique: true}},
			}},
		})
	}
}

// runOneEvents are seqs 1-7: jobs a, b and c start and end (a in 30ms, b
// failing in 50ms, c in 20ms), interleaved, and d starts and is still running.
func runOneEvents() []jobEvent {
	return []jobEvent{
		{ID: "1", Job: "a", Phase: "start"},
		{ID: "2", Job: "b", Phase: "start"},
		{ID: "3", Job: "a", Phase: "end", Status: "ok", Millis: 30},
		{ID: "4", Job: "c", Phase: "start"},
		{ID: "5", Job: "b", Phase: "end", Status: "failed", Millis: 50},
		{ID: "6", Job: "c", Phase: "end", Status: "ok", Millis: 20},
		{ID: "7", Job: "d", Phase: "start"},
	}
}

func jobRow(job string, events, ended, totalMs, lastSeq float64) map[string]any {
	return map[string]any{"job": job, "events": events, "ended": ended, "totalMs": totalMs, "lastSeq": lastSeq}
}

func viewPath(kind, view string) string {
	return "/api/v1/profile/" + url.PathEscape("trace-results/"+kind+"/"+view)
}

var _ = Describe("views over a record result type", Ordered, func() {
	var server followServer

	BeforeAll(func() {
		schemas := recordstore.NewSchemas()
		server = newFollowServerWith(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: localSettings(""), Source: kvRouter(schemas),
			Schemas: schemas, Register: registerJobTypes(jobViews()),
		})
		ctx := forTenant("a")
		_, err := recordstore.AppendTyped(ctx, server.results.Backend, "run-1", "job_event", runOneEvents())
		Expect(err).ToNot(HaveOccurred())
		_, err = recordstore.AppendTyped(ctx, server.results.Backend, "run-2", "job_event", []jobEvent{
			{ID: "1", Job: "z", Phase: "start"}, {ID: "2", Job: "z", Phase: "end", Status: "ok", Millis: 5},
		})
		Expect(err).ToNot(HaveOccurred())
		_, err = recordstore.AppendTyped(ctx, server.results.Backend, "audit-1", "audit_event", []auditEvent{{Job: "a"}, {Job: "a"}})
		Expect(err).ToNot(HaveOccurred())
	})

	get := func(tenant, target string) *http.Response {
		return server.do(http.MethodGet, target, http.Header{"Accept": {"application/json"}, tenantHeader: {tenant}})
	}
	rows := func(kind, view, query string) ([]map[string]any, http.Header) {
		response := get("a", viewPath(kind, view)+"?"+query)
		var body bytes.Buffer
		_, err := body.ReadFrom(response.Body)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())
		ExpectWithOffset(1, response.StatusCode).To(Equal(http.StatusOK), body.String())
		var result []map[string]any
		ExpectWithOffset(1, json.Unmarshal(body.Bytes(), &result)).To(Succeed(), body.String())
		return result, response.Header
	}
	failure := func(tenant, target string) (int, string) {
		response := get(tenant, target)
		var body bytes.Buffer
		_, err := body.ReadFrom(response.Body)
		ExpectWithOffset(1, err).ToNot(HaveOccurred())
		return response.StatusCode, body.String()
	}

	It("compiles a view as a read-only profile over the index, its used views materialized first in dependency order", func() {
		profile, err := server.results.Registry.Get(context.Background(), "trace-results/job_event/overview")
		Expect(err).ToNot(HaveOccurred())
		statement := profile.Query
		Expect(statement).To(HavePrefix("WITH stream_rows AS (\nSELECT "))
		Expect(statement).To(ContainSubstring(" = {{.params.stream}} AND "))
		Expect(statement).To(ContainSubstring(" > {{.params.afterSeq}} AND "))
		Expect(statement).To(ContainSubstring(" <= {{.params.toSeq}}\n)"))
		jobs := strings.Index(statement, "jobs AS MATERIALIZED (")
		busy := strings.Index(statement, "busy_jobs AS MATERIALIZED (")
		slow := strings.Index(statement, "slow AS (")
		Expect([]int{jobs, busy, slow}).To(HaveEach(BeNumerically(">", 0)))
		Expect(jobs).To(BeNumerically("<", busy))
		Expect(busy).To(BeNumerically("<", slow))
		Expect(strings.Count(statement, "WITH slow")).To(BeZero(), "the view's own WITH is merged into the outer one")

		profile.Query = ""
		Expect(profile).To(Equal(query.Profile{
			Name: "trace-results/job_event/overview", Virtual: true, ReadOnly: true,
			Provider: query.ProviderConfig{Type: "sqlite", Connection: "connection://trace-results/index"},
			Params: []query.ParamDef{
				{Name: "stream", Label: "Stream", Required: true, Description: "The record stream to read"},
				{Name: "afterSeq", Label: "After seq", Type: query.ParamTypeNumber, Default: int64(0), Description: "Read the rows after this seq"},
				{Name: "toSeq", Label: "Through seq", Type: query.ParamTypeNumber, Default: int64(9223372036854775807), Description: "Read the rows up to and including this seq"},
				{Name: "minMs", Label: "Slow from", Type: query.ParamTypeNumber, Required: true},
			},
			Columns: []query.ColumnDef{{Name: "scope"}, numberColumn("jobs"), numberColumn("busy"), numberColumn("slow")},
			Order:   query.Order{{Column: "scope", Unique: true}},
			Limits:  &query.RowLimits{PageSize: 100, MaxPageSize: 500, MaxExportRows: recordresults.MaxExportRows},
			Output:  []string{"table", "json", "ndjson", "yaml", "csv", "markdown", "html", "excel", "pdf"},
		}))
	})

	It("lists each result type once, with its views", func() {
		Expect(server.results.Registry.ResultTypes()).To(Equal([]recordresults.RegisteredResultType{
			{Kind: "audit_event", Title: "Audit events", Profile: "trace-results/audit_event", Views: []recordresults.RegisteredResultView{
				{Name: "jobs", Title: "Audited jobs", Profile: "trace-results/audit_event/jobs"},
			}},
			{Kind: "job_event", Title: "Job events", Profile: "trace-results/job_event", Views: []recordresults.RegisteredResultView{
				{Name: "overview", Title: "Overview", Profile: "trace-results/job_event/overview"},
				{Name: "jobs", Title: "Jobs", Profile: "trace-results/job_event/jobs"},
				{Name: "busy_jobs", Title: "Busy jobs", Profile: "trace-results/job_event/busy_jobs"},
			}},
		}))
	})

	It("groups the stream's rows, in the view's declared order, with the grouped total", func() {
		result, header := rows("job_event", "jobs", "stream=run-1")
		Expect(header.Get("X-Total-Count")).To(Equal("4"))
		Expect(result).To(Equal([]map[string]any{
			jobRow("b", 2, 1, 50, 5), jobRow("a", 2, 1, 30, 3), jobRow("c", 2, 1, 20, 6), jobRow("d", 1, 0, 0, 7),
		}))
	})

	It("pages the grouped rows by cursor", func() {
		first, header := rows("job_event", "jobs", "stream=run-1&limit=2")
		Expect(column(first, "job")).To(Equal([]any{"b", "a"}))
		Expect(header.Get("X-Has-More")).To(Equal("true"))
		Expect(header.Get("X-Total-Count")).To(Equal("4"))

		second, header := rows("job_event", "jobs", "stream=run-1&limit=2&cursor="+url.QueryEscape(header.Get("X-Next-Cursor")))
		Expect(column(second, "job")).To(Equal([]any{"c", "d"}))
		Expect(header.Get("X-Has-More")).To(Equal("false"))
	})

	It("sorts by a requested output column", func() {
		result, _ := rows("job_event", "jobs", "stream=run-1&sort=lastSeq&order=asc")
		Expect(column(result, "job")).To(Equal([]any{"a", "b", "c", "d"}))
	})

	It("filters on an output column, and totals the filtered groups", func() {
		result, header := rows("job_event", "jobs", "stream=run-1&filter.job=a,d")
		Expect(header.Get("X-Total-Count")).To(Equal("2"))
		Expect(result).To(Equal([]map[string]any{jobRow("a", 2, 1, 30, 3), jobRow("d", 1, 0, 0, 7)}))
	})

	DescribeTable("aggregates only the seq window it is given",
		func(window string, expected []map[string]any) {
			result, header := rows("job_event", "jobs", "stream=run-1&"+window)
			Expect(result).To(Equal(expected))
			Expect(header.Get("X-Total-Count")).To(Equal(strconv.Itoa(len(expected))))
		},
		Entry("through seq 5", "toSeq=5", []map[string]any{jobRow("b", 2, 1, 50, 5), jobRow("a", 2, 1, 30, 3), jobRow("c", 1, 0, 0, 4)}),
		Entry("after seq 3", "afterSeq=3", []map[string]any{jobRow("b", 1, 1, 50, 5), jobRow("c", 2, 1, 20, 6), jobRow("d", 1, 0, 0, 7)}),
		Entry("after seq 3 through seq 5", "afterSeq=3&toSeq=5", []map[string]any{jobRow("b", 1, 1, 50, 5), jobRow("c", 1, 0, 0, 4)}),
	)

	DescribeTable("reads through its used views, bounded by the same window",
		func(window string, expected map[string]any) {
			result, _ := rows("job_event", "overview", "stream=run-1&minMs=30&"+window)
			Expect(result).To(Equal([]map[string]any{expected}))
		},
		Entry("the whole stream", "", map[string]any{"scope": "all", "jobs": 4.0, "busy": 3.0, "slow": 2.0}),
		Entry("through seq 3", "toSeq=3", map[string]any{"scope": "all", "jobs": 2.0, "busy": 1.0, "slow": 1.0}),
	)

	It("reads only the stream it names", func() {
		result, _ := rows("job_event", "jobs", "stream=run-2")
		Expect(result).To(Equal([]map[string]any{jobRow("z", 2, 1, 5, 2)}))
		audits, _ := rows("audit_event", "jobs", "stream=audit-1")
		Expect(audits).To(Equal([]map[string]any{{"job": "a", "audits": 2.0}}))
	})

	It("answers a stream of another kind as not found, as the base profile does", func() {
		status, body := failure("a", viewPath("job_event", "jobs")+"?stream=audit-1")
		Expect(status).To(Equal(http.StatusNotFound), body)
		Expect(body).To(ContainSubstring("audit_event"))
	})

	It("answers another environment's stream as not found, as the base profile does", func() {
		status, body := failure("b", viewPath("job_event", "jobs")+"?stream=run-1")
		Expect(status).To(Equal(http.StatusNotFound), body)
		status, body = failure("b", "/api/v1/profile/"+url.PathEscape("trace-results/job_event")+"?stream=run-1")
		Expect(status).To(Equal(http.StatusNotFound), body)
	})

	DescribeTable("refuses the base profile's hierarchy and search params on a view",
		func(param string) {
			status, body := failure("a", viewPath("job_event", "jobs")+"?stream=run-1&"+param)
			Expect(status).To(Equal(http.StatusBadRequest), body)
			Expect(body).To(ContainSubstring(strings.SplitN(param, "=", 2)[0]))
		},
		Entry("roots only", "rootsOnly=true"),
		Entry("search", "q=a"),
	)

	It("follows the base profile of a type with views, and refuses to follow a view", func() {
		header := http.Header{tenantHeader: {"a"}}
		response := server.do(http.MethodPost, "/api/v1/profile/profile-trace-results-job-event/sessions?follow=true&stream=run-1&afterSeq=5", header)
		var body bytes.Buffer
		_, err := body.ReadFrom(response.Body)
		Expect(err).ToNot(HaveOccurred())
		Expect(response.StatusCode).To(Equal(http.StatusCreated), body.String())
		var info query.SessionInfo
		Expect(json.Unmarshal(body.Bytes(), &info)).To(Succeed())
		events, _ := server.subscribe(info.ID, "")
		followed, _ := rowsFrom(events, 2)
		Expect(column(followed, "job")).To(Equal([]any{"c", "d"}))

		response = server.do(http.MethodPost, "/api/v1/profile/profile-trace-results-job-event-jobs/sessions?follow=true&stream=run-1", header)
		body.Reset()
		_, err = body.ReadFrom(response.Body)
		Expect(err).ToNot(HaveOccurred())
		Expect(response.StatusCode).ToNot(Equal(http.StatusCreated), body.String())
		Expect(body.String()).To(ContainSubstring("cannot be followed"))
	})
})
