package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/flanksource/clicky/cache"
	"github.com/flanksource/clicky/formatters"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
)

var _ = ginkgo.Describe("GET /sessions", func() {
	var h *apiHarness

	ginkgo.BeforeEach(func() { h = newAPIHarness() })

	ginkgo.DescribeTable("filters from comma-separated values and repeated keys alike",
		func(rawQuery string, wantIDs []string) {
			Expect(infoIDs(h.list(rawQuery).Items)).To(Equal(wantIDs))
		},
		ginkgo.Entry("comma-separated states", "state=running,stopped", []string{"f", "c", "a"}),
		ginkgo.Entry("repeated state keys", "state=running&state=stopped", []string{"f", "c", "a"}),
		ginkgo.Entry("a glob and an exclusion", "profile=*jvm_trace&state=!failed", []string{"f", "c", "a"}),
		ginkgo.Entry("a label, ascending", "label.target=cycle&order=asc", []string{"b", "c", "f"}),
		ginkgo.Entry("restartOf", "restartOf=c", []string{"f"}),
		ginkgo.Entry("a date-math window", "from=now-59m&to=now-56m", []string{"d", "c", "b"}),
		ginkgo.Entry("view sessions only when asked for by role", "role=view", []string{}),
	)

	ginkgo.It("authorizes before totals and paging, asking once per profile", func() {
		h.unread[sqlProfile] = true
		body := h.list("sort=eventCount&limit=2&offset=1")
		Expect(infoIDs(body.Items)).To(Equal([]string{"a", "d"}))
		Expect(body.Total).To(Equal(4), "the refused sql_xevent session is not counted")
		Expect(h.callsFor(jvmProfile, ActionRead)).To(Equal(1))
		Expect(h.callsFor(sqlProfile, ActionRead)).To(Equal(1))
	})

	ginkgo.It("pages with headers and reports whether the store is shared", func() {
		response := h.do(http.MethodGet, "/api/v1/sessions?sort=eventCount&order=desc&limit=2&offset=1")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Header().Get("Content-Type")).To(Equal("application/json"))
		Expect(response.Header().Get("X-Total-Count")).To(Equal("5"))
		Expect(response.Header().Get("X-Page-Limit")).To(Equal("2"))
		Expect(response.Header().Get("X-Page-Offset")).To(Equal("1"))
		Expect(response.Header().Get("X-Sessions-Shared")).To(Equal("false"))
		var body map[string]json.RawMessage
		Expect(json.Unmarshal(response.Body.Bytes(), &body)).To(Succeed())
		Expect(body).To(HaveLen(3))
		Expect(body["total"]).To(MatchJSON("5"))
		Expect(body["shared"]).To(MatchJSON("false"))
	})

	ginkgo.It("derives the overlays each item carries", func() {
		live := h.track(map[string]string{"target": "oipa"})
		ref, err := h.records.Append(context.Background(), "stream-c", recordstoretest.Kind, []query.Row{{"name": "x"}})
		Expect(err).ToNot(HaveOccurred())
		meta, err := h.records.Meta(context.Background(), "stream-c")
		Expect(err).ToNot(HaveOccurred())
		stopped := contractFixture(h.epoch)[0]
		stopped.Events = &query.EventsRef{Stream: "stream-c", Kind: recordstoretest.Kind, Generation: meta.Generation, From: ref.Window.From, To: ref.Window.To}
		Expect(h.store.Update(context.Background(), "c", stopped.SessionStatus)).To(Succeed())

		byID := map[string]query.SessionInfo{}
		for _, info := range h.list("limit=500").Items {
			byID[info.ID] = info
		}
		Expect(byID[live.ID()]).To(And(
			HaveField("Controllable", true), HaveField("LocalWriter", true), HaveField("Unresponsive", false),
			HaveField("EventsAvailable", false), HaveField("Restartable", false)))
		Expect(byID["c"]).To(And(
			HaveField("Controllable", false), HaveField("LocalWriter", false), HaveField("EventsAvailable", true),
			HaveField("Restartable", true), HaveField("RestartedAs", []string{"f"})))
		Expect(byID["a"]).To(And(HaveField("Unresponsive", true), HaveField("Restartable", false)))
		Expect(byID["b"]).To(HaveField("Restartable", false), "no restarter serves sql_xevent")
	})

	ginkgo.DescribeTable("refuses a malformed list request",
		func(rawQuery, message string) {
			response := h.do(http.MethodGet, "/api/v1/sessions?"+rawQuery)
			Expect(response.Code).To(Equal(http.StatusBadRequest))
			Expect(response.Body.String()).To(ContainSubstring(message))
		},
		ginkgo.Entry("a sort outside the allowlist", "sort=error", `session sort "error" is not one of`),
		ginkgo.Entry("an order that is neither asc nor desc", "order=up", `order "up"`),
		ginkgo.Entry("a limit over 500", "limit=501", "limit 501"),
		ginkgo.Entry("an unreadable from", "from=yesterday-ish", "yesterday-ish"),
		ginkgo.Entry("an unknown parameter", "colour=red", `unknown session list parameter "colour"`),
	)

	ginkgo.It("serves a clicky table whose rows carry an id and whose columns bind the filters", func() {
		response := h.do(http.MethodGet, "/api/v1/sessions?limit=2", "Accept", "application/json+clicky")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Header().Get("Content-Type")).To(Equal("application/json+clicky"))
		Expect(response.Header().Get("X-Total-Count")).To(Equal("5"))

		var document formatters.ClickyDocument
		Expect(json.Unmarshal(response.Body.Bytes(), &document)).To(Succeed())
		Expect(document.Version).To(Equal(1))
		Expect(document.Node.Kind).To(Equal("table"))
		columnNames := make([]string, len(document.Node.Columns))
		filterKeys := map[string]string{}
		for i, column := range document.Node.Columns {
			columnNames[i] = column.Name
			filterKeys[column.Name] = column.FilterKey
		}
		Expect(columnNames).To(Equal([]string{
			"startedAt", "session", "profile", "target", "state", "eventCount", "stoppedAt", "origin",
			"principal", "lineage", "labels", "owner",
		}))
		Expect(filterKeys).To(Equal(map[string]string{
			"state": "state", "session": "", "profile": "profile", "target": "label.target", "origin": "label.origin",
			"principal": "principal", "startedAt": "", "stoppedAt": "", "eventCount": "", "lineage": "",
			"labels": "", "owner": "",
		}))
		Expect(document.Node.Rows).To(HaveLen(2))
		cells := document.Node.Rows[0].Cells
		Expect(cells["_id"].Text).To(Equal("f"))
		Expect(cells["lineage"].Text).To(Equal("restart of c"))
		Expect(cells["state"]).To(And(HaveField("Kind", "badge"), HaveField("FilterValue", "running"),
			HaveField("BadgeValue", "running · unresponsive"), HaveField("BadgeColor", "bg-sky-500/10"),
			HaveField("BadgeText", "text-sky-700"), HaveField("BadgeShape", "pill")))
		Expect(cells["labels"].Kind).To(Equal("map"))
		Expect(cells["labels"].Fields).To(HaveLen(2))
		Expect(cells["labels"].Fields[0]).To(And(HaveField("Name", "origin"), HaveField("Value.Text", "web")))
	})

	ginkgo.It("counts lookup options within the window, after Authorize and the other filters", func() {
		h.unread[sqlProfile] = true
		response := h.do(http.MethodGet, "/api/v1/sessions?__lookup=filters&state=running,stopped&label.origin=web")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		var body struct {
			Filters map[string]struct {
				Label   string                           `json:"label"`
				Type    string                           `json:"type"`
				Multi   bool                             `json:"multi"`
				Options map[string]formatters.ClickyNode `json:"options"`
				Counts  map[string]int                   `json:"counts"`
			} `json:"filters"`
		}
		Expect(json.Unmarshal(response.Body.Bytes(), &body)).To(Succeed())
		Expect(body.Filters["state"].Counts).To(Equal(map[string]int{"running": 1, "stopped": 1}),
			"state counts ignore the state filter but keep origin=web; b is refused")
		Expect(body.Filters["label.origin"].Counts).To(Equal(map[string]int{"web": 2, "testplan": 1}))
		Expect(body.Filters["profile"].Counts).To(Equal(map[string]int{jvmProfile: 2}))
		Expect(body.Filters["state"]).To(And(HaveField("Type", "multi-filter"), HaveField("Multi", true)))
		Expect(body.Filters["restartOf"]).To(And(HaveField("Type", "value"), HaveField("Multi", false),
			HaveField("Counts", map[string]int{"c": 1})))
		Expect(body.Filters["state"].Options["running"]).To(Equal(formatters.ClickyNode{Kind: "text", Text: "running", Plain: "running"}))

		narrowed := h.do(http.MethodGet, "/api/v1/sessions?__lookup=filters&__lookup_filter=label.origin&__lookup_q="+url.QueryEscape("TEST"))
		Expect(narrowed.Code).To(Equal(http.StatusOK), narrowed.Body.String())
		Expect(narrowed.Body.String()).To(MatchJSON(`{"filters":{"label.origin":{"label":"origin","type":"multi-filter","multi":true,
			"options":{"testplan":{"kind":"text","text":"testplan","plain":"testplan"}},"counts":{"testplan":1}}}}`))
	})

	ginkgo.It("negotiates the list's format from ?format as profile lists do", func() {
		table := h.do(http.MethodGet, "/api/v1/sessions?limit=2&format=clicky-json")
		Expect(table.Code).To(Equal(http.StatusOK), table.Body.String())
		Expect(table.Header().Get("Content-Type")).To(Equal("application/json+clicky"))

		plain := h.do(http.MethodGet, "/api/v1/sessions?limit=2&format=json", "Accept", "application/json+clicky")
		Expect(plain.Header().Get("Content-Type")).To(Equal("application/json"), "?format outranks Accept")

		refused := h.do(http.MethodGet, "/api/v1/sessions?format=csv")
		Expect(refused.Code).To(Equal(http.StatusBadRequest))
		Expect(refused.Body.String()).To(ContainSubstring(`session list format "csv"`))
	})

	ginkgo.It("lets the store page when no live session could be on the page, and pages itself otherwise", func() {
		recording := &filterRecordingStore{SessionStore: h.store}
		h.handler.sessions = recording

		body := h.list("sort=eventCount&limit=2&offset=1")
		Expect(infoIDs(body.Items)).To(Equal([]string{"b", "a"}))
		Expect(body.Total).To(Equal(5))
		Expect(recording.last()).To(And(HaveField("Limit", 2), HaveField("Offset", 1)))

		h.track(map[string]string{"target": "oipa"})
		body = h.list("sort=eventCount&limit=2&offset=1")
		Expect(body.Total).To(Equal(6))
		Expect(recording.last()).To(And(HaveField("Limit", 0), HaveField("Offset", 0)),
			"a live session replaces its stored copy, so the merge is paged here")
	})

	ginkgo.It("reads each stored record at most once to list a page with its lineage", func() {
		counting := &countingCache{Store: cache.NewMemory(), reads: map[string]int{}}
		store, err := NewKVStore(func(context.Context) (cache.Store, string, error) { return counting, "", nil }, KVStoreOptions{})
		Expect(err).ToNot(HaveOccurred())
		for _, rec := range contractFixture(h.epoch) {
			Expect(store.Begin(context.Background(), rec)).To(Succeed())
		}
		h.handler.sessions = store
		counting.reset()

		body := h.list("limit=500")
		Expect(body.Items).To(HaveLen(5))
		for _, rec := range contractFixture(h.epoch) {
			Expect(counting.readsOf(rec.ID)).To(Equal(1), "session %s", rec.ID)
		}
	})

	ginkgo.It("keeps the window when a session started before it", func() {
		body := h.list("from=" + url.QueryEscape(time.Now().Add(-57*time.Minute-30*time.Second).Format(time.RFC3339)))
		Expect(infoIDs(body.Items)).To(Equal([]string{"f", "d"}))
	})
})
