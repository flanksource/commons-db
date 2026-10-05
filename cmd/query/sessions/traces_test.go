// Specs for the trace plugin routes: listing the kinds a server serves, and
// starting a capture of one as a session the session API then controls.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	profilepkg "github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/probe"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/recordstore/recordresults/recordresultstest"
	"github.com/flanksource/commons-db/tracing/traces"
)

type pingParams struct {
	Count int `json:"count"`
}

func (p pingParams) Validate() error {
	if p.Count < 0 {
		return fmt.Errorf("count must not be negative")
	}
	return nil
}

type ping struct {
	At time.Time `json:"at"`
	N  int       `json:"n"`
}

// pings emits Count pings, then runs until it is stopped.
type pings struct{}

func (pings) Params() pingParams { return pingParams{Count: 1} }

func (pings) Schema() recordresults.ResultType[ping] {
	return recordresults.ResultType[ping]{Title: "Pings", TimeColumn: "at"}
}

func (pings) Handle(ctx dbcontext.Context, p pingParams, records traces.Emitter[ping], _ traces.Records[ping]) error {
	for n := 1; n <= p.Count; n++ {
		if err := records.Emit(ctx, ping{At: time.Now().UTC(), N: n}); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return nil
}

type traceHarness struct {
	handler  *sessionHandler
	registry *query.SessionRegistry
	results  *recordresults.Results

	mu      sync.Mutex
	refused map[string]Action
}

func newTraceHarness() *traceHarness {
	h := &traceHarness{refused: map[string]Action{}}
	kinds := traces.NewKinds()
	Expect(kinds.RegisterKind("pings", traces.NewHandler[pingParams, ping](pings{}, traces.Capabilities{Live: true}))).To(Succeed())
	h.results = recordresultstest.OpenResults(recordresults.OpenOptions{
		Prefix: "traces", ConnectionName: "traces",
		Settings: recordresultstest.LocalSettings(recordstore.BackendSQLite), Register: kinds.RegisterResultTypes,
	})
	runtime := &traces.Runtime{
		Kinds: kinds, Results: h.results, Probes: probe.NewManager(probe.ManagerOptions{}), PollEvery: 20 * time.Millisecond,
	}
	h.registry = query.NewSessionRegistry(query.RegistryOptions{})
	runtime.Sessions = h.registry
	ginkgo.DeferCleanup(func() { Expect(h.registry.StopAll(context.Background())).To(Succeed()) })
	profiles, err := profilepkg.NewFileStore(ginkgo.GinkgoT().TempDir())
	Expect(err).ToNot(HaveOccurred())
	h.handler = newSessionHandler(sessionHandlerOptions{
		Prefix: "/api/v1", Ctx: dbcontext.New(), Store: profiles, Registry: h.registry,
		Records: h.results.Backend, Traces: runtime, Authorize: h.authorize, Next: &nextMarker{},
		Principal: func(*http.Request) string { return "tester" },
	})
	return h
}

func (h *traceHarness) authorize(_ *http.Request, profile string, action Action) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if refused, ok := h.refused[profile]; ok && (refused == ActionRead || action == ActionControl) {
		return fmt.Errorf("profile %q is not %s-able by this caller", profile, action)
	}
	return nil
}

func (h *traceHarness) do(method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	response := httptest.NewRecorder()
	h.handler.ServeHTTP(response, request)
	return response
}

var _ = ginkgo.Describe("Trace plugin routes", func() {
	ginkgo.It("lists the kinds it serves with their params form and columns", func() {
		h := newTraceHarness()
		response := h.do(http.MethodGet, "/api/v1/traces/kinds", "")
		Expect(response.Code).To(Equal(http.StatusOK))
		var kinds []traces.KindInfo
		Expect(json.Unmarshal(response.Body.Bytes(), &kinds)).To(Succeed())
		Expect(kinds).To(HaveLen(1))
		Expect(kinds[0].Name).To(Equal("pings"))
		Expect(kinds[0].Params.Properties).To(HaveKey("count"))
		Expect(kinds[0].Columns).ToNot(BeEmpty())
	})

	ginkgo.It("leaves out the kinds a caller may not read", func() {
		h := newTraceHarness()
		h.refused["traces/pings"] = ActionRead
		response := h.do(http.MethodGet, "/api/v1/traces/kinds", "")
		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(strings.TrimSpace(response.Body.String())).To(Equal("[]"))
	})

	ginkgo.It("starts a capture as a session the session API stops, its records committed and sealed", func() {
		h := newTraceHarness()
		response := h.do(http.MethodPost, "/api/v1/traces/pings/sessions", `{"params": {"count": 2}, "duration": "5m"}`)
		Expect(response.Code).To(Equal(http.StatusCreated), response.Body.String())
		var info query.SessionInfo
		Expect(json.Unmarshal(response.Body.Bytes(), &info)).To(Succeed())
		Expect(info.Profile).To(Equal("traces/pings"))
		Expect(info.Principal).To(Equal("tester"))
		Expect(info.StopAt).ToNot(BeNil())
		Expect(*info.StopAt).To(BeTemporally("~", time.Now().Add(5*time.Minute), time.Minute))

		session, ok := h.registry.Get(info.ID)
		Expect(ok).To(BeTrue())
		Eventually(func() int64 { return session.Snapshot().EventCount }).WithTimeout(5 * time.Second).Should(Equal(int64(2)))

		Expect(h.do(http.MethodPost, "/api/v1/sessions/"+info.ID+"/stop", "").Code).To(Equal(http.StatusOK))
		Eventually(session.Done()).WithTimeout(10 * time.Second).Should(BeClosed())
		ended := session.Snapshot()
		Expect(ended.State).To(Equal(query.SessionStopped))
		meta, err := h.results.Backend.Meta(context.Background(), ended.Events.Stream)
		Expect(err).ToNot(HaveOccurred())
		Expect(meta.Sealed).To(BeTrue())
		Expect(meta.Total).To(Equal(int64(2)))

		events := h.do(http.MethodGet, "/api/v1/sessions/"+info.ID+"/events?format=ndjson", "")
		Expect(events.Code).To(Equal(http.StatusOK))
		Expect(strings.Count(strings.TrimSpace(events.Body.String()), "\n")).To(Equal(1))
	})

	ginkgo.DescribeTable("refuses a start it cannot serve",
		func(path, body string, status int, message string) {
			h := newTraceHarness()
			response := h.do(http.MethodPost, path, body)
			Expect(response.Code).To(Equal(status))
			Expect(response.Body.String()).To(ContainSubstring(message))
			Expect(h.registry.List()).To(BeEmpty())
		},
		ginkgo.Entry("an unknown kind", "/api/v1/traces/nope/sessions", `{}`, http.StatusNotFound, "unknown trace kind"),
		ginkgo.Entry("params the kind refuses", "/api/v1/traces/pings/sessions", `{"params": {"count": -1}}`, http.StatusBadRequest, "must not be negative"),
		ginkgo.Entry("a params field the kind lacks", "/api/v1/traces/pings/sessions", `{"params": {"cnt": 1}}`, http.StatusBadRequest, "cnt"),
		ginkgo.Entry("a body field the route lacks", "/api/v1/traces/pings/sessions", `{"parms": {}}`, http.StatusBadRequest, "parms"),
		ginkgo.Entry("a duration that is not one", "/api/v1/traces/pings/sessions", `{"duration": "soon"}`, http.StatusBadRequest, "duration"),
	)

	ginkgo.It("refuses a start the caller may not control", func() {
		h := newTraceHarness()
		h.refused["traces/pings"] = ActionControl
		response := h.do(http.MethodPost, "/api/v1/traces/pings/sessions", `{}`)
		Expect(response.Code).To(Equal(http.StatusForbidden))
		Expect(h.registry.List()).To(BeEmpty())
	})
})
