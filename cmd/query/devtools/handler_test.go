package devtools_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons-db/cmd/query/devtools"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons/har"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fallthroughHandler stands in for the rest of the server chain, so a spec can
// assert that a request devtools does not own reaches it untouched.
type fallthroughHandler struct {
	reached bool
	armed   bool
}

func (f *fallthroughHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.reached = true
	f.armed = devtools.RecorderFromRequest(r) != nil
	w.WriteHeader(http.StatusTeapot)
}

func newHandler(store *devtools.Store, enabled bool) (http.Handler, *fallthroughHandler) {
	next := &fallthroughHandler{}
	handler := devtools.New(devtools.HandlerOptions{
		Prefix: "/api/v1", Store: store, Enabled: enabled,
	}).Handler(next)
	return handler, next
}

func get(handler http.Handler, target string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
}

var _ = Describe("Handler", func() {
	var store *devtools.Store
	var handler http.Handler
	var next *fallthroughHandler

	BeforeEach(func() {
		store = devtools.NewStore(devtools.Options{})
		handler, next = newHandler(store, true)
	})

	It("passes through anything it does not own", func() {
		response := get(handler, "/api/v1/profile/orders")
		Expect(next.reached).To(BeTrue())
		Expect(response.Code).To(Equal(http.StatusTeapot))
	})

	It("arms profile sample filter-value lookups", func() {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/profile/sample/filters/values", nil)
		request.Header.Set(devtools.LevelHeader, "debug")
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		Expect(next.armed).To(BeTrue())
		Expect(response.Header().Get(devtools.IDHeader)).NotTo(BeEmpty())
		Expect(store.Records(0)).To(HaveLen(1))
		Expect(store.Records(0)[0].Source.Surface).To(Equal("sample"))
	})

	It("arms connection browser filter-value lookups", func() {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/connection/logs/browser/filters/values", nil)
		request.Header.Set(devtools.LevelHeader, "debug")
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		Expect(next.armed).To(BeTrue())
		Expect(response.Header().Get(devtools.IDHeader)).NotTo(BeEmpty())
		Expect(store.Records(0)).To(HaveLen(1))
		Expect(store.Records(0)[0].Source.Surface).To(Equal("browser"))
	})

	It("arms reconciliation actions", func() {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/profiles/Orders%20Emitted/reconcile", nil)
		request.Header.Set(devtools.LevelHeader, "debug")
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		Expect(next.armed).To(BeTrue())
		Expect(response.Header().Get(devtools.IDHeader)).NotTo(BeEmpty())
		Expect(store.Records(0)).To(HaveLen(1))
		Expect(store.Records(0)[0].Source.Surface).To(Equal("reconcile"))
		Expect(store.Records(0)[0].Source.Profile).To(Equal("Orders Emitted"))
	})

	It("records an implicit successful response status", func() {
		implicit := devtools.New(devtools.HandlerOptions{
			Prefix: "/api/v1", Store: store, Enabled: true,
		}).Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}))
		request := httptest.NewRequest(http.MethodGet, "/api/v1/profile/orders", nil)
		request.Header.Set(devtools.LevelHeader, "debug")

		implicit.ServeHTTP(httptest.NewRecorder(), request)

		Expect(store.Records(0)).To(HaveLen(1))
		Expect(store.Records(0)[0].Status).To(Equal(http.StatusOK))
	})

	It("does not answer a path that merely starts with the same letters", func() {
		get(handler, "/api/v1/devtoolsy")
		Expect(next.reached).To(BeTrue())
	})

	// A server told to hide error details must not hand out queries, headers and
	// response bodies through a side door.
	It("is invisible when disabled", func() {
		disabled, disabledNext := newHandler(store, false)
		response := get(disabled, "/api/v1/devtools/records")
		Expect(response.Code).To(Equal(http.StatusNotFound))
		Expect(disabledNext.reached).To(BeFalse())
	})

	It("advertises the levels it accepts rather than making a client hardcode them", func() {
		var capabilities devtools.Capabilities
		Expect(json.Unmarshal(get(handler, "/api/v1/devtools").Body.Bytes(), &capabilities)).To(Succeed())
		Expect(capabilities.Enabled).To(BeTrue())
		Expect(capabilities.Header).To(Equal("X-Debug-Level"))
		Expect(capabilities.Levels).To(ContainElements("off", "debug", "trace2"))
	})

	Describe("records", func() {
		BeforeEach(func() {
			store.Add(recorderWithBody("rec-1", 64))
			store.Add(recorderWithBody("rec-2", 64))
		})

		It("returns the history with what the store has let go", func() {
			var page devtools.RecordsPage
			Expect(json.Unmarshal(get(handler, "/api/v1/devtools/records").Body.Bytes(), &page)).To(Succeed())
			Expect(page.Records).To(HaveLen(2))
			Expect(page.Stats.Records).To(Equal(2))
		})

		It("returns only what came after a sequence the caller holds", func() {
			var page devtools.RecordsPage
			Expect(json.Unmarshal(get(handler, "/api/v1/devtools/records?after=1").Body.Bytes(), &page)).To(Succeed())
			Expect(page.Records).To(HaveLen(1))
			Expect(page.Records[0].ID).To(Equal("rec-2"))
		})

		It("rejects an unparseable cursor instead of silently starting over", func() {
			response := get(handler, "/api/v1/devtools/records?after=soon")
			Expect(response.Code).To(Equal(http.StatusBadRequest))
			Expect(response.Body.String()).To(ContainSubstring("expected a record sequence"))
		})

		It("serves one record's detail", func() {
			var detail query.ExecutionDetail
			Expect(json.Unmarshal(get(handler, "/api/v1/devtools/records/rec-1").Body.Bytes(), &detail)).To(Succeed())
			Expect(detail.Summary.ID).To(Equal("rec-1"))
			Expect(detail.HAR.Log.Entries).To(HaveLen(1))
		})

		It("serves a record's HAR as an importable file", func() {
			response := get(handler, "/api/v1/devtools/records/rec-1/har")
			Expect(response.Header().Get("Content-Disposition")).To(ContainSubstring(`filename="rec-1.har"`))

			var file har.File
			Expect(json.Unmarshal(response.Body.Bytes(), &file)).To(Succeed())
			Expect(file.Log.Version).To(Equal("1.2"))
			Expect(file.Log.Entries).To(HaveLen(1))
		})

		It("answers an unknown id with not-found", func() {
			Expect(get(handler, "/api/v1/devtools/records/nope").Code).To(Equal(http.StatusNotFound))
		})

		// Gone, not Not-Found: the id was real, the detail aged out. Conflating
		// them would tell a user their id was wrong when it was not.
		It("answers evicted detail with gone and says why", func() {
			brief := devtools.NewStore(devtools.Options{DetailTTL: time.Nanosecond})
			brief.Add(recorderWithBody("old", 64))
			time.Sleep(time.Millisecond)
			brief.Add(recorderWithBody("new", 64))
			briefHandler, _ := newHandler(brief, true)

			response := get(briefHandler, "/api/v1/devtools/records/old")
			Expect(response.Code).To(Equal(http.StatusGone))

			var body map[string]any
			Expect(json.Unmarshal(response.Body.Bytes(), &body)).To(Succeed())
			Expect(body["code"]).To(Equal("detail_evicted"))
			Expect(body["reason"]).To(ContainSubstring("older than"))
		})

		It("clears the history on delete", func() {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/api/v1/devtools/records", nil))
			Expect(recorder.Code).To(Equal(http.StatusNoContent))
			Expect(store.Records(0)).To(BeEmpty())
		})
	})

	Describe("streaming", func() {
		It("replays the history then follows", func() {
			store.Add(recorderWithBody("rec-1", 64))

			request, cancel := streamRequest("/api/v1/devtools/stream")
			recorder, stop := serveStream(handler, request, cancel)

			Eventually(recorder.Body).Should(ContainSubstring("event: record"))
			store.Add(recorderWithBody("rec-2", 64))
			Eventually(recorder.Body).Should(ContainSubstring(`"id":"rec-2"`))
			stop()

			Expect(recorder.Header().Get("Content-Type")).To(Equal("text/event-stream"))
			Expect(recorder.Body()).To(ContainSubstring("id: 1"))
		})

		It("resumes past a sequence the client already applied", func() {
			store.Add(recorderWithBody("rec-1", 64))
			store.Add(recorderWithBody("rec-2", 64))

			request, cancel := streamRequest("/api/v1/devtools/stream")
			request.Header.Set("Last-Event-ID", "1")
			recorder, stop := serveStream(handler, request, cancel)

			Eventually(recorder.Body).Should(ContainSubstring(`"id":"rec-2"`))
			stop()
			Expect(recorder.Body()).ToNot(ContainSubstring(`"id":"rec-1"`))
		})

		It("rejects a resume header that is not a sequence", func() {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/devtools/stream", nil)
			request.Header.Set("Last-Event-ID", "yesterday")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			Expect(recorder.Code).To(Equal(http.StatusBadRequest))
		})

		It("offers the same records as a downloadable file", func() {
			store.Add(recorderWithBody("rec-1", 64))
			response := get(handler, "/api/v1/devtools/stream?format=ndjson")
			Expect(response.Header().Get("Content-Type")).To(Equal("application/x-ndjson"))
			Expect(strings.TrimSpace(response.Body.String())).To(ContainSubstring(`"id":"rec-1"`))
		})

		It("tails process logs on their own endpoint", func() {
			store.Log(query.LogLine{Source: "process", Level: "warn", Message: "disk filling"})

			request, cancel := streamRequest("/api/v1/devtools/logs")
			recorder, stop := serveStream(handler, request, cancel)

			Eventually(recorder.Body).Should(ContainSubstring("disk filling"))
			stop()
			Expect(recorder.Body()).To(ContainSubstring("event: log"))
		})
	})
})

// streamRequest builds a cancellable request so a spec can end a stream the way
// a browser does — by going away — rather than waiting for a keepalive.
func streamRequest(target string) (*http.Request, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	return httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx), cancel
}

// streamRecorder is httptest.ResponseRecorder made safe to read while it is
// being written. A stream is served on one goroutine and polled from the spec's,
// and ResponseRecorder's bytes.Buffer is not safe for that.
type streamRecorder struct {
	mu     sync.Mutex
	header http.Header
	body   strings.Builder
	code   int
}

func newStreamRecorder() *streamRecorder {
	return &streamRecorder{header: http.Header{}, code: http.StatusOK}
}

func (s *streamRecorder) Header() http.Header { return s.header }

func (s *streamRecorder) WriteHeader(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.code = code
}

func (s *streamRecorder) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body.Write(b)
}

func (s *streamRecorder) Flush() {}

func (s *streamRecorder) Body() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.body.String()
}

func (s *streamRecorder) Code() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.code
}

// serveStream runs handler against a cancellable request and returns the
// recorder plus a stop function that cancels and waits for the handler to
// return, so a spec never asserts against a stream still being written.
func serveStream(handler http.Handler, request *http.Request, cancel func()) (*streamRecorder, func()) {
	recorder := newStreamRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(recorder, request)
	}()
	return recorder, func() {
		cancel()
		Eventually(done).Should(BeClosed())
	}
}
