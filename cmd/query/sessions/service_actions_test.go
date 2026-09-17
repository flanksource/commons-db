package sessions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
)

var _ = ginkgo.Describe("session actions", func() {
	var h *apiHarness

	ginkgo.BeforeEach(func() { h = newAPIHarness() })

	ginkgo.It("serves one session with its overlays and lineage, and answers a caller who may not read it 404", func() {
		response := h.do(http.MethodGet, "/api/v1/sessions/c")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(decodeInfo(response)).To(And(HaveField("State", query.SessionStopped),
			HaveField("Restartable", true), HaveField("RestartedAs", []string{"f"})))

		Expect(h.do(http.MethodGet, "/api/v1/sessions/nope").Code).To(Equal(http.StatusNotFound))
		h.unread[jvmProfile] = true
		refused := h.do(http.MethodGet, "/api/v1/sessions/c")
		Expect(refused.Code).To(Equal(http.StatusNotFound))
		Expect(refused.Body.String()).To(Equal("session \"c\" not found\n"))
		Expect(h.do(http.MethodPost, "/api/v1/sessions/c/restart").Code).To(Equal(http.StatusNotFound),
			"a control on a session the caller cannot read does not confirm it exists either")
	})

	ginkgo.It("names in restartedAs only the restarted sessions the caller may read", func() {
		hidden := contractRecord(h.epoch, "g", sqlProfile, query.SessionRunning, 6, 0, nil)
		hidden.RestartOf = "c"
		Expect(h.store.Begin(context.Background(), hidden)).To(Succeed())
		Expect(decodeInfo(h.do(http.MethodGet, "/api/v1/sessions/c")).RestartedAs).To(Equal([]string{"f", "g"}))

		h.unread[sqlProfile] = true
		Expect(decodeInfo(h.do(http.MethodGet, "/api/v1/sessions/c")).RestartedAs).To(Equal([]string{"f"}))
		byID := map[string]query.SessionInfo{}
		for _, info := range h.list("limit=500").Items {
			byID[info.ID] = info
		}
		Expect(byID["c"].RestartedAs).To(Equal([]string{"f"}))
	})

	ginkgo.It("answers a connection trace the caller may not start 404, authorizing before it looks the connection up", func() {
		const connection = "5b0c4c1e-6b1f-4c1a-9f0e-2d3c4b5a6978"
		profile := "connection-" + connection + "-sql-xevent"
		h.uncontrol[profile] = true
		request := httptest.NewRequest(http.MethodPost, "/api/v1/connection/"+connection+"/trace/sessions", strings.NewReader(`{}`))
		response := httptest.NewRecorder()
		h.handler.ServeHTTP(response, request)

		Expect(response.Code).To(Equal(http.StatusNotFound))
		Expect(response.Body.String()).To(Equal(`connection "` + connection + `" not found` + "\n"))
		Expect(h.callsFor(profile, ActionControl)).To(Equal(1))
	})

	ginkgo.It("stops a live session under control authorization only", func() {
		live := h.track(nil)
		h.uncontrol[jvmProfile] = true
		refused := h.do(http.MethodPost, "/api/v1/sessions/"+live.ID()+"/stop")
		Expect(refused.Code).To(Equal(http.StatusForbidden), refused.Body.String())
		Expect(live.Snapshot().State).To(Equal(query.SessionRunning), "a refused stop leaves the session running")

		h.uncontrol[jvmProfile] = false
		response := h.do(http.MethodPost, "/api/v1/sessions/"+live.ID()+"/stop")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(decodeInfo(response).State).To(BeElementOf(query.SessionStopping, query.SessionStopped))
		Eventually(live.Done()).Should(BeClosed())
		Expect(live.Snapshot()).To(And(HaveField("State", query.SessionStopped), HaveField("StopReason", "stopped by tester")))

		Expect(h.do(http.MethodPost, "/api/v1/sessions/"+live.ID()+"/stop").Code).To(Equal(http.StatusConflict))
		Expect(h.do(http.MethodPost, "/api/v1/sessions/a/stop").Code).To(Equal(http.StatusConflict), "a is not live here")
		Expect(h.do(http.MethodPost, "/api/v1/sessions/nope/stop").Code).To(Equal(http.StatusNotFound))
	})

	ginkgo.It("no longer stops a session by DELETE", func() {
		live := h.track(nil)
		h.do(http.MethodDelete, "/api/v1/sessions/"+live.ID())
		Expect(h.handler.next.(*nextMarker).hit).To(BeTrue())
		Expect(live.Snapshot().State).To(Equal(query.SessionRunning))
	})

	ginkgo.It("extends a live session by adding the duration to its deadline", func() {
		live := h.track(nil)
		before := *live.Snapshot().StopAt

		Expect(h.do(http.MethodPost, "/api/v1/sessions/"+live.ID()+"/extend").Code).To(Equal(http.StatusBadRequest))
		Expect(h.do(http.MethodPost, "/api/v1/sessions/"+live.ID()+"/extend?duration=soon").Code).To(Equal(http.StatusBadRequest))
		response := h.do(http.MethodPost, "/api/v1/sessions/"+live.ID()+"/extend?duration=3m")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(*decodeInfo(response).StopAt).To(BeTemporally("~", before.Add(3*time.Minute), time.Second))

		Expect(h.do(http.MethodPost, "/api/v1/sessions/c/extend?duration=3m").Code).To(Equal(http.StatusConflict))
	})

	ginkgo.It("restarts for the duration first requested when no duration is given", func() {
		requested := contractFixture(h.epoch)[0]
		requested.ID, requested.State = "requested", query.SessionStopped
		requested.Params = map[string]any{"class": "com.example.Listener", "durationMs": 120000.0}
		extended := requested.StartedAt.Add(14 * time.Minute)
		requested.StopAt = &extended
		Expect(h.store.Begin(context.Background(), requested)).To(Succeed())

		response := h.do(http.MethodPost, "/api/v1/sessions/requested/restart")
		Expect(response.Code).To(Equal(http.StatusCreated), response.Body.String())
		Expect(*decodeInfo(response).StopAt).To(BeTemporally("~", time.Now().Add(2*time.Minute), 5*time.Second))
	})

	ginkgo.It("clamps an extension to the registry's maximum duration from the start", func() {
		live := h.track(nil)
		response := h.do(http.MethodPost, "/api/v1/sessions/"+live.ID()+"/extend?duration=10h")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		info := decodeInfo(response)
		Expect(*info.StopAt).To(BeTemporally("~", info.StartedAt.Add(query.DefaultMaxDuration), time.Second))
	})

	ginkgo.It("restarts an ended, restartable session as a new one", func() {
		response := h.do(http.MethodPost, "/api/v1/sessions/c/restart?duration=2m")
		Expect(response.Code).To(Equal(http.StatusCreated), response.Body.String())
		next := decodeInfo(response)
		Expect(next).To(And(HaveField("RestartOf", "c"), HaveField("Principal", "tester"),
			HaveField("Controllable", true), HaveField("Labels", contractFixture(h.epoch)[0].Labels)))
		Expect(next.ID).ToNot(Equal("c"))
		Expect(*next.StopAt).To(BeTemporally("~", time.Now().Add(2*time.Minute), 5*time.Second))

		Expect(h.do(http.MethodPost, "/api/v1/sessions/a/restart").Code).To(Equal(http.StatusConflict), "a is still running")
		Expect(h.do(http.MethodPost, "/api/v1/sessions/b/restart").Code).To(Equal(http.StatusConflict), "no restarter for sql_xevent")
		Expect(h.do(http.MethodPost, "/api/v1/sessions/c/restart?duration=-1m").Code).To(Equal(http.StatusBadRequest))
		h.uncontrol[jvmProfile] = true
		Expect(h.do(http.MethodPost, "/api/v1/sessions/c/restart").Code).To(Equal(http.StatusForbidden))
	})
})
