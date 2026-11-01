// Specs for the query trace kind: a saved profile and a typed provider entry
// read once, a prometheus range windowed onto the read, every appending only
// rows past the last one it kept, untilStopped reading at the stop, and the
// params a capture could not read with refused.

package query_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	dbquery "github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/profilestore"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/query"
	"github.com/flanksource/commons-db/tracing/tracingtest"
)

// savedProfiles is the host's profile store, by name.
type savedProfiles map[string]dbquery.Profile

func (s savedProfiles) Resolve(_ context.Context, name string) (dbquery.Profile, error) {
	if profile, ok := s[name]; ok {
		return profile, nil
	}
	return dbquery.Profile{}, fmt.Errorf("profile %q not found", name)
}

// gate records every read the kind prepares.
type gate struct {
	mu    sync.Mutex
	reads []profilestore.ReadRequest
}

func (g *gate) BeforeExecute(_ context.Context, reads []profilestore.ReadRequest) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reads = append(g.reads, reads...)
	return func() {}, nil
}

func (g *gate) Reads() []profilestore.ReadRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]profilestore.ReadRequest(nil), g.reads...)
}

// stamp is how the specs' SQL rows spell a time: what SQLite's julianday reads.
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// windowedRows is a SQL read of rows at the given times, bounded by the window
// inclusively at both ends, so a re-read returns the row it ended on again.
func windowedRows(rows map[string]time.Time) string {
	values := ""
	for id, at := range rows {
		if values != "" {
			values += ", "
		}
		values += fmt.Sprintf("('%s', '%s')", id, stamp(at))
	}
	return fmt.Sprintf(`SELECT column1 AS id, column2 AS timestamp FROM (VALUES %s)
		WHERE julianday(column2) >= julianday({{.params.from}}) AND julianday(column2) <= julianday({{.params.to}})`, values)
}

func sqlEntry(statement string, extra string) string {
	encoded, err := json.Marshal(statement)
	Expect(err).ToNot(HaveOccurred())
	return fmt.Sprintf(`{"provider": {"type": "sql", "sql": {"type": "sqlite", "url": ":memory:"}, "query": %s}%s}`, encoded, extra)
}

// recorded is the provider row a record carries.
func recorded(row recordstore.Row) map[string]any {
	switch value := row["row"].(type) {
	case map[string]any:
		return value
	case string:
		var decoded map[string]any
		Expect(json.Unmarshal([]byte(value), &decoded)).To(Succeed())
		return decoded
	}
	Fail(fmt.Sprintf("record row is %T", row["row"]))
	return nil
}

func ids(rows []recordstore.Row) []string {
	var out []string
	for _, row := range rows {
		out = append(out, fmt.Sprint(recorded(row)["id"]))
	}
	return out
}

var _ = Describe("query trace kind", func() {
	var env tracingtest.Env
	var reads *gate
	saved := savedProfiles{
		"work-items": {
			Name:     "work-items",
			Provider: dbquery.ProviderConfig{Type: "sqlite", Options: map[string]any{"url": ":memory:"}},
			Query:    `SELECT column1 AS id, column2 AS time FROM (VALUES ('w1', '2026-10-05T09:00:00Z'), ('w2', '2026-10-05T09:01:00Z'))`,
		},
	}

	BeforeEach(func() {
		reads = &gate{}
		kind, err := query.Kind(query.Options{Profiles: saved, BeforeExecute: reads.BeforeExecute})
		Expect(err).ToNot(HaveOccurred())
		env = tracingtest.NewEnv(map[string]tracing.TracePlugin{"query": kind})
	})

	It("reads a saved profile through the host's resolver, behind BeforeExecute", func() {
		info := tracingtest.Ended(env.Start("query", `{"profile": "work-items"}`))
		Expect(info.Error).To(BeEmpty())
		Expect(info.State).To(Equal(dbquery.SessionCompleted))
		rows := env.Rows(info)
		Expect(ids(rows)).To(Equal([]string{"w1", "w2"}))
		Expect(rows[0]["profile"]).To(Equal("work-items"))
		Expect(rows[0]["provider"]).To(Equal("sqlite"))
		Expect(fmt.Sprint(rows[1]["time"])).To(ContainSubstring("2026-10-05"))
		Expect(reads.Reads()).To(HaveLen(1))
		Expect(reads.Reads()[0].Profile.Name).To(Equal("work-items"))
	})

	It("reads a typed sql entry in place", func() {
		info := tracingtest.Ended(env.Start("query", sqlEntry(`SELECT 'a' AS id`, "")))
		Expect(info.Error).To(BeEmpty())
		rows := env.Rows(info)
		Expect(ids(rows)).To(Equal([]string{"a"}))
		Expect(rows[0]["provider"]).To(Equal("sql"))
	})

	It("windows a prometheus range onto the read", func() {
		var asked []string
		var mu sync.Mutex
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			Expect(r.ParseForm()).To(Succeed())
			mu.Lock()
			asked = append(asked, r.URL.Path, r.Form.Get("start"), r.Form.Get("end"), r.Form.Get("step"))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"matrix","result":[`+
				`{"metric":{"pod":"a"},"values":[[1790000000,"1"],[1790000060,"2"]]}]}}`)
		}))
		DeferCleanup(server.Close)
		params := fmt.Sprintf(`{"provider": {"type": "prometheus", "prometheus": {"url": %q, "range": {"step": "1m"}}, "query": "up"}, "from": "now-1h"}`, server.URL)
		info := tracingtest.Ended(env.Start("query", params))
		Expect(info.Error).To(BeEmpty())
		Expect(env.Rows(info)).To(HaveLen(2))

		mu.Lock()
		defer mu.Unlock()
		Expect(asked).To(HaveLen(4))
		Expect(asked[0]).To(Equal("/api/v1/query_range"))
		start, end := unixSeconds(asked[1]), unixSeconds(asked[2])
		Expect(end - start).To(BeNumerically("~", time.Hour.Seconds(), 5))
		Expect(end).To(BeNumerically("~", float64(time.Now().Unix()), 10))
		Expect(asked[3]).To(Equal("60"))
	})

	It("re-reads every interval, appending only rows past the last one it kept", func() {
		now := time.Now()
		statement := windowedRows(map[string]time.Time{
			"past": now.Add(-500 * time.Millisecond), "next": now.Add(300 * time.Millisecond), "later": now.Add(700 * time.Millisecond),
		})
		session := env.Start("query", sqlEntry(statement, `, "from": "now-1s", "every": "50ms"`))
		Eventually(func() int64 { return session.Snapshot().EventCount }).WithTimeout(5 * time.Second).Should(Equal(int64(3)))
		Consistently(func() int64 { return session.Snapshot().EventCount }).WithTimeout(300 * time.Millisecond).Should(Equal(int64(3)))
		session.Stop("spec")
		info := tracingtest.Ended(session)
		Expect(info.State).To(Equal(dbquery.SessionStopped))
		Expect(ids(env.Rows(info))).To(Equal([]string{"past", "next", "later"}))
		Expect(len(reads.Reads())).To(BeNumerically(">", 3), "every read is gated")
	})

	It("reads once when stopped, over the whole capture", func() {
		now := time.Now()
		statement := windowedRows(map[string]time.Time{
			"before": now.Add(-time.Hour), "during": now.Add(150 * time.Millisecond), "after": now.Add(time.Hour),
		})
		session := env.Start("query", sqlEntry(statement, `, "untilStopped": true`))
		tracingtest.Running(session)
		time.Sleep(300 * time.Millisecond)
		Expect(reads.Reads()).To(BeEmpty(), "nothing is read before the stop")
		session.Stop("spec")
		info := tracingtest.Ended(session)
		Expect(info.Error).To(BeEmpty())
		Expect(ids(env.Rows(info))).To(Equal([]string{"during"}))
	})

	It("fails a capture read every interval whose rows carry no time column", func() {
		session := env.Start("query", sqlEntry(`SELECT 'a' AS id WHERE {{.params.from}} IS NOT NULL AND {{.params.to}} IS NOT NULL`, `, "every": "50ms"`))
		info := tracingtest.Ended(session)
		Expect(info.State).To(Equal(dbquery.SessionFailed))
		Expect(info.Error).To(ContainSubstring("neither time nor timestamp"))
	})

	DescribeTable("refuses params it could not read with",
		func(raw, message string) {
			kind, err := query.Kind(query.Options{Profiles: saved, BeforeExecute: (&gate{}).BeforeExecute})
			Expect(err).ToNot(HaveOccurred())
			Expect(kind.ValidateParams(json.RawMessage(raw))).To(MatchError(ContainSubstring(message)))
		},
		Entry("neither a profile nor a provider", `{}`, "exactly one of profile or provider"),
		Entry("both a profile and a provider", `{"profile": "p", "provider": {"type": "sql"}}`, "exactly one of profile or provider"),
		Entry("every and untilStopped", `{"profile": "p", "every": "1s", "untilStopped": true}`, "every"),
		Entry("an every that is no duration", `{"profile": "p", "every": "often"}`, "every"),
		Entry("a to with no from", `{"profile": "p", "to": "now"}`, "to"),
		Entry("a provider type it does not read", `{"provider": {"type": "loki"}}`, `provider type "loki"`),
		Entry("options of another provider", `{"provider": {"type": "prometheus", "sql": {"type": "sqlite"}}}`, "sql options"),
		Entry("the sqlite key with another engine", `{"provider": {"type": "sqlite", "sql": {"type": "postgres"}}}`, "provider sql"),
		Entry("invalid typed options", `{"provider": {"type": "http", "http": {"method": "FETCH"}}}`, "unsupported http method"),
		Entry("an unbounded in-place read every interval", `{"provider": {"type": "sql", "query": "SELECT 1"}, "every": "1s"}`, "neither params.from nor params.to"),
	)

	It("refuses at start a saved profile the host does not hold", func() {
		_, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: "query", Params: json.RawMessage(`{"profile": "missing"}`)})
		Expect(err).To(MatchError(ContainSubstring(`profile "missing" not found`)))
	})

	It("refuses a kind with no BeforeExecute to gate its reads", func() {
		_, err := query.Kind(query.Options{Profiles: saved})
		Expect(err).To(MatchError(ContainSubstring("BeforeExecute")))
	})
})

func unixSeconds(value string) float64 {
	parsed, err := strconv.ParseFloat(value, 64)
	Expect(err).ToNot(HaveOccurred())
	return parsed
}
