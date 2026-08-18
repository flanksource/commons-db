package providers_test

import (
	gocontext "context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

var _ = Describe("streaming capability", func() {
	It("reports a provider that cannot follow its source as not streaming", func() {
		Expect(query.SupportsStreaming("sql")).To(BeFalse())
		Expect(query.SupportsStreaming("nonexistent")).To(BeFalse())
	})

	It("reports the log backends that can tail", func() {
		for _, typ := range []string{"loki", "k8s"} {
			Expect(query.SupportsStreaming(typ)).To(BeTrue(), fmt.Sprintf("provider %q should stream", typ))
		}
	})
})
