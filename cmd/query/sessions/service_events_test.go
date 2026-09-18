package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
)

var _ = ginkgo.Describe("events and results of a session no longer live", func() {
	var h *apiHarness
	ctx := context.Background()

	// recordEvents appends rows to stream and points session id's status at
	// them, as a capture's final status would.
	recordEvents := func(id, stream string, rows int, state query.SessionState) {
		batch := make([]query.Row, rows)
		for i := range batch {
			batch[i] = query.Row{"name": "row", "count": float64(i + 1)}
		}
		appended, err := h.records.Append(ctx, stream, recordstoretest.Kind, batch)
		Expect(err).ToNot(HaveOccurred())
		meta, err := h.records.Meta(ctx, stream)
		Expect(err).ToNot(HaveOccurred())
		rec, _, err := h.store.Get(ctx, id)
		Expect(err).ToNot(HaveOccurred())
		rec.State = state
		rec.Events = &query.EventsRef{
			Stream: stream, Kind: recordstoretest.Kind, Generation: meta.Generation, From: appended.Window.From, To: appended.Window.To,
			Store: query.EventsStoreLocation{Backend: "sqlite", Host: "pod-a", File: "/data/records.sqlite"},
		}
		rec.Result = json.RawMessage(`{"calls":3}`)
		Expect(h.store.Update(ctx, id, rec.SessionStatus)).To(Succeed())
	}

	ginkgo.BeforeEach(func() { h = newAPIHarness() })

	ginkgo.It("replays a terminal session's recorded window as SSE, resuming after Last-Event-ID", func() {
		recordEvents("c", "stream-c", 3, query.SessionStopped)

		body := h.do(http.MethodGet, "/api/v1/sessions/c/events").Body.String()
		Expect(sseFrames(body, "event")).To(Equal(3))
		Expect(sseFrames(body, "done")).To(Equal(1))
		Expect(body).To(ContainSubstring(`"row":{"count":2,"name":"row"}`))

		resumed := h.do(http.MethodGet, "/api/v1/sessions/c/events", "Last-Event-ID", "2").Body.String()
		Expect(sseFrames(resumed, "event")).To(Equal(1))
		Expect(resumed).To(ContainSubstring("id: 3\n"))
	})

	ginkgo.It("exports the recorded window as NDJSON", func() {
		recordEvents("c", "stream-c", 2, query.SessionStopped)
		response := h.do(http.MethodGet, "/api/v1/sessions/c/events?format=ndjson")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		lines := strings.Split(strings.TrimSpace(response.Body.String()), "\n")
		Expect(lines).To(HaveLen(2))
		var event query.Event
		Expect(json.Unmarshal([]byte(lines[1]), &event)).To(Succeed())
		Expect(event).To(And(HaveField("SessionID", "c"), HaveField("Sequence", int64(2))))
	})

	ginkgo.It("tails a record this process writes until its stream is sealed", func() {
		rec := contractFixture(h.epoch)[4]
		rec.ID, rec.Owner = "local", h.registry.Owner()
		Expect(h.store.Begin(ctx, rec)).To(Succeed())
		recordEvents("local", "stream-local", 1, query.SessionRunning)

		done := make(chan string)
		go func() {
			defer ginkgo.GinkgoRecover()
			done <- h.do(http.MethodGet, "/api/v1/sessions/local/events").Body.String()
		}()
		Consistently(done, 100*time.Millisecond).ShouldNot(Receive())
		_, err := h.records.Append(ctx, "stream-local", recordstoretest.Kind, []query.Row{{"name": "late"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(h.records.Seal(ctx, "stream-local")).To(Succeed())

		var body string
		Eventually(done, 5*time.Second).Should(Receive(&body))
		Expect(sseFrames(body, "event")).To(Equal(2))
		Expect(body).To(ContainSubstring(`"name":"late"`))
	})

	ginkgo.It("answers 410 with the store location when the recorded stream is gone", func() {
		recordEvents("c", "stream-c", 1, query.SessionStopped)
		rec, _, err := h.store.Get(ctx, "c")
		Expect(err).ToNot(HaveOccurred())
		rec.Events.Stream = "stream-expired"
		Expect(h.store.Update(ctx, "c", rec.SessionStatus)).To(Succeed())

		response := h.do(http.MethodGet, "/api/v1/sessions/c/events")
		Expect(response.Code).To(Equal(http.StatusGone))
		Expect(response.Body.String()).To(MatchJSON(`{
			"error": "session c: record stream \"stream-expired\" is gone: record stream not found",
			"store": {"backend": "sqlite", "host": "pod-a", "file": "/data/records.sqlite"}}`))
	})

	ginkgo.It("answers 410, and reports no events, when the stream id now holds another generation", func() {
		recordEvents("c", "stream-c", 2, query.SessionStopped)
		rec, _, err := h.store.Get(ctx, "c")
		Expect(err).ToNot(HaveOccurred())
		recorded := rec.Events.Generation
		rec.Events.Generation = "generation-that-expired"
		Expect(h.store.Update(ctx, "c", rec.SessionStatus)).To(Succeed())

		for _, path := range []string{"/api/v1/sessions/c/events", "/api/v1/sessions/c/events?format=ndjson"} {
			response := h.do(http.MethodGet, path)
			Expect(response.Code).To(Equal(http.StatusGone), path)
			Expect(response.Body.String()).To(MatchJSON(`{
				"error": "session c: record stream \"stream-c\" generation \"generation-that-expired\" is gone; the stream now holds generation \"` + recorded + `\"",
				"store": {"backend": "sqlite", "host": "pod-a", "file": "/data/records.sqlite"}}`))
		}
		Expect(decodeInfo(h.do(http.MethodGet, "/api/v1/sessions/c")).EventsAvailable).To(BeFalse())
	})

	ginkgo.It("streams a large NDJSON export row by row, never past the recorded window", func() {
		const recorded = 250
		recordEvents("c", "stream-c", recorded, query.SessionStopped)
		_, err := h.records.Append(ctx, "stream-c", recordstoretest.Kind, []query.Row{{"name": "after the window"}})
		Expect(err).ToNot(HaveOccurred())

		response := h.do(http.MethodGet, "/api/v1/sessions/c/events?format=ndjson", "Last-Event-ID", "10")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Header().Get("Content-Type")).To(Equal("application/x-ndjson"))
		lines := strings.Split(strings.TrimSpace(response.Body.String()), "\n")
		Expect(lines).To(HaveLen(recorded - 10))
		Expect(response.Body.String()).ToNot(ContainSubstring("after the window"))
	})

	ginkgo.It("ends a follow when the live session ends without sealing its stream, after draining it", func() {
		live := h.track(nil)
		_, err := h.records.Append(ctx, "stream-live", recordstoretest.Kind, []query.Row{{"name": "first"}})
		Expect(err).ToNot(HaveOccurred())
		meta, err := h.records.Meta(ctx, "stream-live")
		Expect(err).ToNot(HaveOccurred())
		ref := &query.EventsRef{Stream: "stream-live", Kind: recordstoretest.Kind, Generation: meta.Generation, From: 1}
		Expect(live.Running(query.RunningUpdate{Events: ref})).To(Succeed())

		done := make(chan string)
		go func() {
			defer ginkgo.GinkgoRecover()
			done <- h.do(http.MethodGet, "/api/v1/sessions/"+live.ID()+"/events").Body.String()
		}()
		Consistently(done, 100*time.Millisecond).ShouldNot(Receive())
		_, err = h.records.Append(ctx, "stream-live", recordstoretest.Kind, []query.Row{{"name": "last"}})
		Expect(err).ToNot(HaveOccurred())
		live.Abort(errors.New("event log unwritable")) // no seal

		var body string
		Eventually(done, 5*time.Second).Should(Receive(&body))
		Expect(sseFrames(body, "event")).To(Equal(2))
		Expect(body).To(ContainSubstring(`"name":"last"`))
		Expect(sseFrames(body, "done")).To(Equal(1))
		Expect(body).To(ContainSubstring(`"state":"failed"`))
	})

	ginkgo.It("serves the recorded result, and nothing for a session that recorded none", func() {
		recordEvents("c", "stream-c", 1, query.SessionStopped)
		response := h.do(http.MethodGet, "/api/v1/sessions/c/result")
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Body.String()).To(MatchJSON(`{"calls":3}`))

		Expect(h.do(http.MethodGet, "/api/v1/sessions/d/result").Code).To(Equal(http.StatusNotFound))
		Expect(h.do(http.MethodGet, "/api/v1/sessions/d/events").Code).To(Equal(http.StatusNotFound))
	})

	ginkgo.It("answers a caller who may not read the profile exactly as for a session that does not exist", func() {
		recordEvents("c", "stream-c", 1, query.SessionStopped)
		h.unread[jvmProfile] = true
		for _, route := range []string{"", "/events", "/result"} {
			refused := h.do(http.MethodGet, "/api/v1/sessions/c"+route)
			missing := h.do(http.MethodGet, "/api/v1/sessions/nope"+route)
			Expect(refused.Code).To(Equal(http.StatusNotFound), route)
			Expect(missing.Code).To(Equal(http.StatusNotFound), route)
			Expect(strings.ReplaceAll(refused.Body.String(), `"c"`, `"nope"`)).To(Equal(missing.Body.String()), route)
		}
	})
})
