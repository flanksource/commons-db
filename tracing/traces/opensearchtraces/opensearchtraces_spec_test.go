// Specs for the opensearch trace kind: an import of a window ends by itself, a
// follow picks up spans indexed later, and a span read twice is stored once.

package opensearchtraces_test

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"

	"github.com/flanksource/commons-db/tracing/traces"
	"github.com/flanksource/commons-db/tracing/traces/opensearchtraces"
	"github.com/flanksource/commons-db/tracing/traces/tracestest"
)

type span struct {
	id, traceID, service string
	at                   time.Time
}

// spanIndex serves span documents as an index serves a search_after walk:
// ascending by @timestamp then _id, strictly past the request's position.
type spanIndex struct {
	server *httptest.Server
	mu     sync.Mutex
	spans  []span
}

func newSpanIndex(spans ...span) *spanIndex {
	index := &spanIndex{spans: spans}
	index.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer GinkgoRecover()
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/_field_caps") {
			_, _ = fmt.Fprintf(w, `{"fields":{%q:{"date":{"searchable":true,"aggregatable":true}}}}`, r.URL.Query().Get("fields"))
			return
		}
		if strings.Contains(r.URL.Path, "/_mapping/field/") {
			_, _ = fmt.Fprint(w, `{"otel-traces-2026":{"mappings":{"@timestamp":{"full_name":"@timestamp","mapping":{"@timestamp":{"type":"date"}}}}}}`)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = fmt.Fprint(w, index.hits(body))
	}))
	DeferCleanup(index.server.Close)
	return index
}

func (i *spanIndex) hits(body map[string]any) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	afterMS, afterID := int64(math.MinInt64), ""
	if after, ok := body["search_after"].([]any); ok && len(after) == 2 {
		afterMS = int64(after[0].(float64))
		afterID = after[1].(string)
	}
	var rendered []string
	for _, s := range i.spans {
		at := s.at.UnixMilli()
		if at < afterMS || (at == afterMS && s.id <= afterID) {
			continue
		}
		rendered = append(rendered, fmt.Sprintf(
			`{"_index":"otel-traces-2026","_id":%q,"_source":{"@timestamp":%q,"trace_id":%q,"span_id":%q,"service_name":%q,"duration_ms":4},"sort":[%d,%q]}`,
			s.id, s.at.Format(time.RFC3339Nano), s.traceID, s.id, s.service, at, s.id))
	}
	return fmt.Sprintf(`{"took":1,"hits":{"total":{"value":%d,"relation":"eq"},"hits":[%s]}}`, len(rendered), strings.Join(rendered, ","))
}

func (i *spanIndex) write(s span) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.spans = append(i.spans, s)
}

var at = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)

var _ = Describe("opensearch trace kind", func() {
	var index *spanIndex
	var env tracestest.Env

	BeforeEach(func() {
		index = newSpanIndex(
			span{id: "s1", traceID: "t1", service: "checkout", at: at},
			span{id: "s2", traceID: "t1", service: "payments", at: at.Add(time.Second)},
		)
		env = tracestest.NewEnv(map[string]traces.TracePlugin{"opensearch": opensearchtraces.Kind()})
	})

	start := func(params string) *query.Session {
		ctx := dbcontext.New().WithConnectionResolver(func(_ dbcontext.Context, reference string) (*models.Connection, error) {
			switch reference {
			case "connection://traces":
				return &models.Connection{Name: "traces", Type: models.ConnectionTypeOpenTelemetry,
					Properties: types.JSONStringMap{"connection": "connection://spans"}}, nil
			case "connection://spans":
				return &models.Connection{Name: "spans", Type: models.ConnectionTypeOpenSearch, URL: index.server.URL}, nil
			}
			return nil, nil
		})
		managed, err := env.Runtime.Start(ctx, traces.StartRequest{Kind: "opensearch", Params: json.RawMessage(params)})
		Expect(err).ToNot(HaveOccurred())
		return managed.Session()
	}

	It("imports a window's spans and ends by itself", func() {
		info := tracestest.Ended(start(`{"connection": "connection://traces", "from": "now-1y"}`))
		Expect(info.Error).To(BeEmpty())
		Expect(info.State).To(Equal(query.SessionCompleted))
		rows := env.Rows(info)
		Expect(rows).To(HaveLen(2))
		Expect(rows[0]["traceId"]).To(Equal("t1"))
		Expect(rows[0]["spanId"]).To(Equal("s1"))
		Expect(rows[0]["service"]).To(Equal("checkout"))
		Expect(rows[0]["sourceIndex"]).To(Equal("otel-traces-2026"))
		Expect(rows[0]["sourceId"]).To(Equal("s1"))
		Expect(rows[1]["service"]).To(Equal("payments"))
	})

	It("follows the index until stopped, storing the spans indexed after it caught up", func() {
		session := start(`{"connection": "connection://traces", "follow": true, "poll": "20ms"}`)
		Eventually(func() int64 { return session.Snapshot().EventCount }).WithTimeout(5 * time.Second).Should(Equal(int64(2)))
		index.write(span{id: "s3", traceID: "t2", service: "checkout", at: at.Add(2 * time.Second)})
		Eventually(func() int64 { return session.Snapshot().EventCount }).WithTimeout(5 * time.Second).Should(Equal(int64(3)))
		session.Stop("spec")
		info := tracestest.Ended(session)
		Expect(info.State).To(Equal(query.SessionStopped))
		Expect(env.Rows(info)).To(HaveLen(3))
		Expect(env.Sealed(info)).To(BeTrue())
	})

	It("stores a span read twice once", func() {
		index.write(span{id: "s1", traceID: "t1", service: "checkout", at: at.Add(-time.Second)})
		info := tracestest.Ended(start(`{"connection": "connection://traces"}`))
		Expect(info.Error).To(BeEmpty())
		Expect(env.Rows(info)).To(HaveLen(2))
	})

	DescribeTable("refuses params it could not read with",
		func(raw, message string) {
			Expect(opensearchtraces.Kind().ValidateParams(json.RawMessage(raw))).To(MatchError(ContainSubstring(message)))
		},
		Entry("no connection", `{}`, "connection"),
		Entry("a follow with an end", `{"connection": "c", "follow": true, "to": "now"}`, "follow"),
		Entry("a poll that is not a duration", `{"connection": "c", "poll": "often"}`, "poll"),
		Entry("an unknown format", `{"connection": "c", "format": "zipkin"}`, "format"),
	)
})
