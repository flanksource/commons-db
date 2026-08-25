package providers_test

import (
	gocontext "context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
)

// streamer is the capability under test: a provider that can follow its source
// rather than answer one question and stop.
func streamer(typ string) query.StreamProvider {
	GinkgoHelper()
	provider, err := query.GetProvider(typ)
	Expect(err).ToNot(HaveOccurred())
	streaming, ok := provider.(query.StreamProvider)
	Expect(ok).To(BeTrue(), "provider %q does not stream", typ)
	return streaming
}

// tailContext rebuilds a stub's connection context around a stop the spec
// controls, because a tail ends only when its caller ends it.
func tailContext(ctx dbcontext.Context) (dbcontext.Context, gocontext.CancelFunc) {
	base, cancel := gocontext.WithCancel(gocontext.Background())
	return dbcontext.NewContext(base).WithDB(ctx.DB(), nil), cancel
}

// tail runs a stream in the background — the call blocks until the source ends
// or the caller stops it — and hands back the rows and the outcome.
func tail(ctx dbcontext.Context, provider query.StreamProvider, req query.ProviderRequest) (chan query.Row, chan error) {
	rows := make(chan query.Row, 64)
	done := make(chan error, 1)
	go func() {
		defer GinkgoRecover()
		done <- provider.Stream(ctx, req, func(row query.Row) { rows <- row })
	}()
	return rows, done
}

// --- Loki --------------------------------------------------------------------

// lokiTailStub upgrades the tail endpoint to a websocket and writes whatever a
// spec pushes at it. That is the only protocol Loki serves a live tail over —
// the endpoint a range query is answered on cannot follow at all — so a stub
// speaking anything else would be testing a backend that does not exist.
type lokiTailStub struct {
	server  *httptest.Server
	live    chan any
	queries chan url.Values
	stop    chan struct{}
}

func newLokiTailStub() *lokiTailStub {
	stub := &lokiTailStub{
		live:    make(chan any, 8),
		queries: make(chan url.Values, 4),
		stop:    make(chan struct{}),
	}
	upgrader := websocket.Upgrader{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer GinkgoRecover()
		Expect(r.URL.Path).To(Equal("/loki/api/v1/tail"))
		stub.queries <- r.URL.Query()

		conn, err := upgrader.Upgrade(w, r, nil)
		Expect(err).ToNot(HaveOccurred())
		defer conn.Close()
		for {
			select {
			case response := <-stub.live:
				Expect(conn.WriteJSON(response)).To(Succeed())
			case <-stub.stop:
				return
			}
		}
	}))
	return stub
}

// close ends the handler before the server waits on it: a hijacked websocket is
// no longer a request httptest can time out.
func (s *lokiTailStub) close() {
	close(s.stop)
	s.server.Close()
}

func (s *lokiTailStub) context() dbcontext.Context {
	return dbcontext.New().WithDB(connectionsDB(models.Connection{
		ID: uuid.New(), Name: "loki", Type: models.ConnectionTypeLoki, URL: s.server.URL,
	}), nil)
}

var _ = Describe("loki provider streaming", func() {
	var stub *lokiTailStub

	BeforeEach(func() {
		stub = newLokiTailStub()
		DeferCleanup(stub.close)
	})

	It("maps a tailed line the same way a query maps one", func() {
		ctx, cancel := tailContext(stub.context())
		rows, done := tail(ctx, streamer("loki"), query.ProviderRequest{
			Connection: "connection://loki",
			Query:      `{app="checkout"}`,
		})

		stub.live <- map[string]any{"streams": []any{map[string]any{
			"stream": map[string]string{"app": "checkout", "detected_level": "error", "pod": "checkout-1"},
			"values": [][]string{{"1700000000000000000", "payment failed"}},
		}}}

		var row query.Row
		Eventually(rows, "10s", "10ms").Should(Receive(&row))
		Expect(row).To(HaveKeyWithValue("message", "payment failed"))
		Expect(row).To(HaveKeyWithValue("timestamp", time.Unix(0, 1700000000000000000)))
		// The mapping a range query applies is the mapping a tail applies: the
		// severity comes from detected_level, the host from the pod label, and
		// the stream's other labels stay as columns of their own.
		Expect(row).To(HaveKeyWithValue("severity", "error"))
		Expect(row).To(HaveKeyWithValue("host", "checkout-1"))
		Expect(row).To(HaveKeyWithValue("app", "checkout"))

		// The tail stays open until the caller stops it, and stopping it is not
		// a failure.
		Consistently(done, "200ms").ShouldNot(Receive())
		cancel()
		Eventually(done, "10s", "10ms").Should(Receive(BeNil()))
	})

	It("asks the tail endpoint for the query and the backfill the options declare", func() {
		ctx, cancel := tailContext(stub.context())
		DeferCleanup(cancel)
		_, _ = tail(ctx, streamer("loki"), query.ProviderRequest{
			Connection: "connection://loki",
			Query:      `{app="checkout"}`,
			Options:    map[string]any{"limit": "5", "start": "2026-04-19T11:00:00Z"},
		})

		var asked url.Values
		Eventually(stub.queries, "10s", "10ms").Should(Receive(&asked))
		Expect(asked.Get("query")).To(Equal(`{app="checkout"}`))
		Expect(asked.Get("limit")).To(Equal("5"))
		Expect(asked.Get("start")).To(Equal("2026-04-19T11:00:00Z"))
	})

	// A backfill size nobody can read would otherwise be the difference between
	// the bound the profile declared and Loki's own default, silently.
	It("refuses a limit that is not a number instead of tailing without one", func() {
		err := streamer("loki").Stream(stub.context(), query.ProviderRequest{
			Connection: "connection://loki",
			Query:      `{app="checkout"}`,
			Options:    map[string]any{"limit": "lots"},
		}, func(query.Row) {})
		Expect(err).To(MatchError(ContainSubstring(`limit "lots"`)))
	})
})

// --- Kubernetes --------------------------------------------------------------

// at is the instant the stub's nth line carries, which is half of the position
// a page that ended on that line hands back.
func (s *kubeletStub) at(index int) time.Time {
	GinkgoHelper()
	stamp, _, _ := strings.Cut(s.lines[index], " ")
	parsed, err := time.Parse(time.RFC3339Nano, stamp)
	Expect(err).ToNot(HaveOccurred())
	return parsed
}

var _ = Describe("k8s provider streaming", func() {
	var stub *kubeletStub

	BeforeEach(func() {
		stub = newKubeletStub()
		DeferCleanup(stub.server.Close)
	})

	// drain collects rows until the tail has produced the line a spec waits for,
	// so an assertion about a live line does not race the backfill a tail opens
	// with.
	drain := func(rows chan query.Row, until string) []query.Row {
		GinkgoHelper()
		var collected []query.Row
		Eventually(func() []string {
			for {
				select {
				case row := <-rows:
					collected = append(collected, row)
				default:
					return rowValues(collected, "message")
				}
			}
		}, "20s", "20ms").Should(ContainElement(until))
		return collected
	}

	k8sRequest := func() query.ProviderRequest {
		return query.ProviderRequest{
			Connection: "connection://kube",
			Query:      "kind=Pod namespace=prod name=logs",
			Order:      query.Order{{Column: "timestamp"}, {Column: "id", Unique: true}},
		}
	}

	It("tails every line the container has written and then the ones it writes next", func() {
		ctx, cancel := tailContext(stub.context())
		rows, done := tail(ctx, streamer("k8s"), k8sRequest())

		stub.write(time.Now().UTC().Format(time.RFC3339Nano) + " a7")
		collected := drain(rows, "a7")

		Expect(rowValues(collected, "message")).To(Equal(append(append([]string{}, conformanceIDs...), "a7")))
		// A streamed row is the row a page would have carried, attribution and
		// all — otherwise a live view and a paged one show different columns.
		Expect(collected[0]).To(HaveKeyWithValue("pod", "logs"))
		Expect(collected[0]).To(HaveKeyWithValue("namespace", "prod"))
		Expect(collected[0]).To(HaveKeyWithValue("container", "app"))
		Expect(collected[0]).To(HaveKeyWithValue("id", "prod/logs/app#0"))

		Consistently(done, "200ms").ShouldNot(Receive())
		cancel()
		Eventually(done, "10s", "10ms").Should(Receive(BeNil()))
	})

	It("resumes a page's cursor without repeating the rows that page served", func() {
		ctx, cancel := tailContext(stub.context())
		DeferCleanup(cancel)

		req := k8sRequest()
		req.Position = query.CursorPosition{Keys: []any{stub.at(3), "prod/logs/app#0"}}
		rows, _ := tail(ctx, streamer("k8s"), req)

		stub.write(time.Now().UTC().Format(time.RFC3339Nano) + " a7")
		Expect(rowValues(drain(rows, "a7"), "message")).To(Equal([]string{"a5", "a6", "a7"}))
	})

	// A session that ends the instant it starts is indistinguishable from a
	// source that has gone quiet, which is the wrong thing to show an operator
	// watching for lines that can never come.
	It("refuses to tail a selector that matched no containers", func() {
		req := k8sRequest()
		req.Query = "kind=Pod namespace=prod name=absent"
		err := streamer("k8s").Stream(stub.context(), req, func(query.Row) {})
		Expect(err).To(MatchError(ContainSubstring("nothing to tail")))
	})

	// The kubelet resumes forward from an instant and nothing else, so an order
	// a tail cannot read in is refused rather than answered out of order.
	It("refuses to tail under an order it cannot read in", func() {
		req := k8sRequest()
		req.Order = query.Order{{Column: "timestamp", Desc: true}, {Column: "id", Unique: true}}
		err := streamer("k8s").Stream(stub.context(), req, func(query.Row) {})
		Expect(err).To(MatchError(ContainSubstring("pages only by `timestamp` ascending")))
	})
})

// --- OpenSearch --------------------------------------------------------------

// tailDoc is one document the index holds, in the shape a tail reads it by: an
// instant, an id that breaks ties within it, and a message.
type tailDoc struct {
	id      string
	at      time.Time
	message string
}

// openSearchTailStub serves documents the way an index serves a tail: ascending,
// honouring search_after and size, and growing while it is being read.
//
// It genuinely implements search_after rather than replaying a fixed answer. A
// stub that ignored it would re-serve the backfill on every poll and still pass
// a spec that only counted rows, so none of the specs below could fail for the
// reason they exist.
type openSearchTailStub struct {
	server *httptest.Server

	mu         sync.Mutex
	docs       []tailDoc
	bodies     []map[string]any
	openedPITs int
}

func newOpenSearchTailStub(docs ...tailDoc) *openSearchTailStub {
	stub := &openSearchTailStub{docs: docs}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer GinkgoRecover()
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/_field_caps") {
			_, _ = fmt.Fprintf(w, `{"fields":{%q:{"date":{"searchable":true,"aggregatable":true}}}}`,
				r.URL.Query().Get("fields"))
			return
		}
		if strings.Contains(r.URL.Path, "/_mapping/field/") {
			_, _ = fmt.Fprint(w, `{"logs-2026":{"mappings":{"@timestamp":{"full_name":"@timestamp","mapping":{"@timestamp":{"type":"date","format":"strict_date_optional_time||epoch_millis"}}}}}}`)
			return
		}
		if strings.Contains(r.URL.Path, "/_search/point_in_time") {
			stub.mu.Lock()
			stub.openedPITs++
			stub.mu.Unlock()
			_, _ = fmt.Fprint(w, `{"pit_id":"pit-1","creation_time":1}`)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		size, _ := strconv.Atoi(r.URL.Query().Get("size"))
		_, _ = fmt.Fprint(w, stub.hits(body, size))
	}))
	return stub
}

// hits serves the documents strictly past the search_after position, in the
// direction the body's sort asked for.
//
// Honouring the direction is what makes the tail's choice of order observable
// at all: served ascending regardless, a descending tail would still look like
// it worked, because the stub rather than the provider would be supplying the
// forward walk.
func (s *openSearchTailStub) hits(body map[string]any, size int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bodies = append(s.bodies, body)

	descending := false
	if sort, ok := body["sort"].([]any); ok && len(sort) > 0 {
		if leading, ok := sort[0].(map[string]any); ok {
			for _, spec := range leading {
				if by, ok := spec.(map[string]any); ok && by["order"] == "desc" {
					descending = true
				}
			}
		}
	}

	// The position an unstarted walk resumes past is the far end of whichever
	// direction it reads in.
	afterMS, afterID := int64(math.MinInt64), ""
	if descending {
		afterMS, afterID = int64(math.MaxInt64), "￿"
	}
	if after, ok := body["search_after"].([]any); ok && len(after) == 2 {
		if ms, ok := after[0].(float64); ok {
			afterMS = int64(ms)
		}
		afterID, _ = after[1].(string)
	}

	ordered := make([]tailDoc, len(s.docs))
	copy(ordered, s.docs)
	if descending {
		for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
			ordered[i], ordered[j] = ordered[j], ordered[i]
		}
	}

	rendered := make([]string, 0, len(ordered))
	for _, doc := range ordered {
		at := doc.at.UnixMilli()
		past := at > afterMS || (at == afterMS && doc.id > afterID)
		if descending {
			past = at < afterMS || (at == afterMS && doc.id < afterID)
		}
		if !past {
			continue
		}
		rendered = append(rendered, fmt.Sprintf(
			`{"_index":"logs","_id":%q,"_source":{"@timestamp":%q,"message":%q},"sort":[%d,%q]}`,
			doc.id, doc.at.UTC().Format(time.RFC3339Nano), doc.message, at, doc.id))
		if size > 0 && len(rendered) == size {
			break
		}
	}
	return fmt.Sprintf(`{"took":1,"timed_out":false,"hits":{"total":{"value":%d,"relation":"eq"},"hits":[%s]}}`,
		len(s.docs), strings.Join(rendered, ","))
}

// write indexes one more document, as whatever is shipping logs into the index
// would while a tail is open.
func (s *openSearchTailStub) write(doc tailDoc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs = append(s.docs, doc)
}

func (s *openSearchTailStub) lastBody() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.bodies) == 0 {
		return nil
	}
	return s.bodies[len(s.bodies)-1]
}

func (s *openSearchTailStub) pits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.openedPITs
}

// openSearchRangeBounds collects every range bound a compiled body places on one
// field, wherever in the bool tree it ended up.
//
// It reads the structure rather than the rendered text because the text lies:
// asserting that a body carries no "lte" matches the "lte" inside "filter", so a
// substring check on this shape passes and fails for reasons that have nothing
// to do with the bound.
func openSearchRangeBounds(body map[string]any, field string) map[string]any {
	bounds := map[string]any{}
	if body == nil {
		return bounds
	}
	queue := []any{body["query"]}
	for len(queue) > 0 {
		node, _ := queue[0].(map[string]any)
		queue = queue[1:]
		if node == nil {
			continue
		}
		if boolQuery, ok := node["bool"].(map[string]any); ok {
			for _, occur := range []string{"filter", "must", "should", "must_not"} {
				if clauses, ok := boolQuery[occur].([]any); ok {
					queue = append(queue, clauses...)
				}
			}
			continue
		}
		if rng, ok := node["range"].(map[string]any); ok {
			if on, ok := rng[field].(map[string]any); ok {
				for edge, value := range on {
					bounds[edge] = value
				}
			}
		}
	}
	return bounds
}

var _ = Describe("opensearch provider streaming", func() {
	var stub *openSearchTailStub
	var base time.Time

	BeforeEach(func() {
		base = time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
		stub = newOpenSearchTailStub(
			tailDoc{id: "d1", at: base, message: "a1"},
			tailDoc{id: "d2", at: base.Add(time.Second), message: "a2"},
		)
		DeferCleanup(stub.server.Close)
	})

	// The profile reads newest-first, which is how a log profile is read and
	// the opposite of the only direction a tail can move.
	osRequest := func(extra map[string]any) query.ProviderRequest {
		options := map[string]any{
			"address":  stub.server.URL,
			"index":    "logs",
			"tailPoll": "10ms",
			"search":   map[string]any{"timeField": "@timestamp"},
		}
		for key, value := range extra {
			options[key] = value
		}
		return query.ProviderRequest{
			Options: options,
			Order: query.Order{
				{Column: "@timestamp", Desc: true},
				{Column: "_id", Unique: true},
			},
		}
	}

	// drain collects rows until the one a spec is waiting for has arrived, so an
	// assertion about a live document does not race the backfill the tail opens
	// with.
	drain := func(rows chan query.Row, until string) []query.Row {
		GinkgoHelper()
		var collected []query.Row
		Eventually(func() []string {
			for {
				select {
				case row := <-rows:
					collected = append(collected, row)
				default:
					return rowValues(collected, "message")
				}
			}
		}, "20s", "20ms").Should(ContainElement(until))
		return collected
	}

	It("backfills the window and then emits what is indexed next", func() {
		ctx, cancel := tailContext(dbcontext.New())
		rows, done := tail(ctx, streamer("opensearch"), osRequest(nil))

		Eventually(func() int { return len(stub.lastBody()) }, "10s", "10ms").ShouldNot(BeZero())
		stub.write(tailDoc{id: "d3", at: base.Add(2 * time.Second), message: "a3"})

		// Each document exactly once and in the order they were written: a poll
		// that re-served the backfill would repeat a1 here.
		Expect(rowValues(drain(rows, "a3"), "message")).To(Equal([]string{"a1", "a2", "a3"}))

		Consistently(done, "200ms").ShouldNot(Receive())
		cancel()
		Eventually(done, "10s", "10ms").Should(Receive(BeNil()))
	})

	// The decision this whole provider capability rests on. search_after only
	// walks toward the end of the sort, so a newest-first profile has to be
	// tailed against its own declared order or it cannot be tailed at all.
	It("tails ascending even though the profile reads newest-first", func() {
		ctx, cancel := tailContext(dbcontext.New())
		DeferCleanup(cancel)
		rows, _ := tail(ctx, streamer("opensearch"), osRequest(nil))
		_ = drain(rows, "a2")

		Expect(stub.lastBody()["sort"]).To(Equal([]any{
			map[string]any{"@timestamp": map[string]any{"order": "asc"}},
			map[string]any{"_id": map[string]any{"order": "asc"}},
		}))
	})

	// A frozen view cannot contain a document that did not exist when it was
	// taken, which is every document a tail is waiting for.
	It("never pins a point-in-time", func() {
		ctx, cancel := tailContext(dbcontext.New())
		DeferCleanup(cancel)
		rows, _ := tail(ctx, streamer("opensearch"), osRequest(nil))
		_ = drain(rows, "a2")

		Expect(stub.pits()).To(BeZero(), "a tail over a frozen view can never see a new document")
	})

	It("maps a tailed document the way a paged one is mapped", func() {
		ctx, cancel := tailContext(dbcontext.New())
		DeferCleanup(cancel)
		rows, _ := tail(ctx, streamer("opensearch"), osRequest(nil))

		collected := drain(rows, "a1")
		Expect(collected[0]).To(HaveKeyWithValue("message", "a1"))
		Expect(collected[0]).To(HaveKeyWithValue("id", "d1"))
		Expect(collected[0]).To(HaveKey("timestamp"))
	})

	It("holds the cursor behind now when tailLag is set", func() {
		request := osRequest(map[string]any{"tailLag": "30s"})
		request.Params = map[string]any{"since": "now-1h"}
		request.ParamRoles = map[string]query.ParamRole{"since": query.ParamRoleTimeFrom}

		ctx, cancel := tailContext(dbcontext.New())
		DeferCleanup(cancel)
		rows, _ := tail(ctx, streamer("opensearch"), request)
		_ = drain(rows, "a1")

		// The lag bound ANDs alongside the window the time-from parameter
		// compiled to, rather than replacing it.
		bounds := openSearchRangeBounds(stub.lastBody(), "@timestamp")
		Expect(bounds).To(HaveKey("gte"), "the profile's own window must survive the lag bound")
		Expect(bounds).To(HaveKey("lte"), "tailLag must bound the time field above")

		// And it is held back by the lag that was asked for, not merely bounded
		// at some instant — an `lte` of now would be no lag at all.
		lte, err := time.Parse(time.RFC3339Nano, fmt.Sprint(bounds["lte"]))
		Expect(err).ToNot(HaveOccurred())
		Expect(lte).To(BeTemporally("<", time.Now().UTC().Add(-25*time.Second)))
	})

	It("refuses a tailLag it cannot spell without a time field", func() {
		request := osRequest(map[string]any{"tailLag": "30s"})
		delete(request.Options, "search")
		request.Query = `{"query":{"match_all":{}}}`

		err := streamer("opensearch").Stream(dbcontext.New(), request, func(query.Row) {})
		Expect(err).To(MatchError(ContainSubstring("tailLag")))
	})

	// A poll interval nobody can read would otherwise be the difference between
	// the cadence the profile declared and this package's default, silently.
	It("refuses a tailPoll that is not a duration", func() {
		err := streamer("opensearch").Stream(dbcontext.New(),
			osRequest(map[string]any{"tailPoll": "often"}), func(query.Row) {})
		Expect(err).To(MatchError(ContainSubstring(`tailPoll "often"`)))
	})

	It("refuses to tail a profile that names no order to follow", func() {
		request := osRequest(nil)
		delete(request.Options, "search")
		request.Query = `{"query":{"match_all":{}}}`
		request.Order = nil

		err := streamer("opensearch").Stream(dbcontext.New(), request, func(query.Row) {})
		Expect(err).To(MatchError(ContainSubstring("no direction to follow")))
	})

	// The whole promotion, from a stored query profile to a live session, rather
	// than the provider method on its own.
	//
	// OpenSearch is the case where that path can break in a way no Stream spec
	// would catch: its window is compiled out of the profile's *parameters* into
	// the search body, not out of runtime filters, so openTailWindow — which
	// only opens filters — does nothing for it. What keeps the tail from
	// stopping at an instant resolved when it started is Follow dropping the
	// time-to parameter before anything is bound. If that ever regressed, every
	// followed OpenSearch profile would quietly tail a closed window and go
	// silent, which reads exactly like a source with nothing to say.
	It("follows a stored profile end to end, with no upper bound left on the window", func() {
		profile := query.Profile{
			Name: "checkout-logs",
			Provider: query.ProviderConfig{
				Type: "opensearch",
				Options: map[string]any{
					"address":  stub.server.URL,
					"index":    "logs",
					"tailPoll": "10ms",
					"search":   map[string]any{"timeField": "@timestamp"},
				},
			},
			Params: []query.ParamDef{
				{Name: "since", Type: query.ParamTypeDateTime, Role: query.ParamRoleTimeFrom, Default: "now-1h"},
				{Name: "until", Type: query.ParamTypeDateTime, Role: query.ParamRoleTimeTo, Default: "now"},
			},
			Order: query.Order{{Column: "@timestamp", Desc: true}, {Column: "_id", Unique: true}},
		}

		registry := query.NewSessionRegistry(query.RegistryOptions{})
		session, err := query.ExecuteStream(dbcontext.New(), registry, query.Follow(profile))
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(session.Stop)

		_, live, cancel := session.Subscribe()
		DeferCleanup(cancel)

		collect := func(until string) []string {
			GinkgoHelper()
			var seen []string
			Eventually(func() []string {
				select {
				case event := <-live:
					if event.Row != nil {
						seen = append(seen, fmt.Sprint(event.Row["message"]))
					}
					for _, row := range event.Rows {
						seen = append(seen, fmt.Sprint(row["message"]))
					}
				default:
				}
				return seen
			}, "20s", "20ms").Should(ContainElement(until))
			return seen
		}

		Expect(collect("a2")).To(ContainElements("a1", "a2"))

		// Still tailing after the backfill drained: a window that kept its `now`
		// upper bound would have ended here instead.
		stub.write(tailDoc{id: "d3", at: time.Now().UTC(), message: "a3"})
		Expect(collect("a3")).To(ContainElement("a3"))

		// And the bound really is absent from what was sent, rather than merely
		// wide enough that the spec did not notice it.
		bounds := openSearchRangeBounds(stub.lastBody(), "@timestamp")
		Expect(bounds).To(HaveKey("gte"), "a tail still starts somewhere")
		Expect(bounds).ToNot(HaveKey("lte"),
			"a followed profile must not carry the upper bound its time-to parameter would have compiled to")
		Expect(bounds).ToNot(HaveKey("lt"))
	})
})

var _ = Describe("streaming capability", func() {
	It("reports a provider that cannot follow its source as not streaming", func() {
		Expect(query.SupportsStreaming("sql")).To(BeFalse())
		Expect(query.SupportsStreaming("nonexistent")).To(BeFalse())
	})

	It("reports the log backends that can tail", func() {
		for _, typ := range []string{"loki", "k8s", "opensearch"} {
			Expect(query.SupportsStreaming(typ)).To(BeTrue(), fmt.Sprintf("provider %q should stream", typ))
		}
	})

	// The tail loop lives on the shared OpenSearch walk, so the trace provider
	// built on that walk is one method away from streaming — and until it has
	// that method, it must not advertise the capability.
	It("does not advertise streaming for the trace provider yet", func() {
		Expect(query.SupportsStreaming("opentelemetry")).To(BeFalse())
	})
})
