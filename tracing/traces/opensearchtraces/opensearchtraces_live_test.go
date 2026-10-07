// Live specs for the opensearch trace kind against a real OpenSearch: an
// import of a window ends by itself, and a follow stores spans indexed later.

package opensearchtraces_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/types"

	"github.com/flanksource/commons-db/tracing/traces"
	"github.com/flanksource/commons-db/tracing/traces/opensearchtraces"
	"github.com/flanksource/commons-db/tracing/traces/tracestest"
)

// liveRequest sends body to the OpenSearch at address, failing the spec on
// anything but a 2xx answer.
func liveRequest(address, method, path, body string) {
	GinkgoHelper()
	request, err := http.NewRequest(method, strings.TrimRight(address, "/")+path, strings.NewReader(body))
	Expect(err).ToNot(HaveOccurred())
	request.Header.Set("Content-Type", "application/json")
	if strings.HasPrefix(path, "/_bulk") {
		request.Header.Set("Content-Type", "application/x-ndjson")
	}
	response, err := http.DefaultClient.Do(request)
	Expect(err).ToNot(HaveOccurred())
	defer func() { _ = response.Body.Close() }()
	answer, _ := io.ReadAll(response.Body)
	Expect(response.StatusCode).To(BeNumerically("<", 300), "%s %s: %s", method, path, answer)
	Expect(string(answer)).ToNot(ContainSubstring(`"errors":true`))
}

// Set COMMONS_DB_OPENSEARCH_URL to run these. Credentials travel in the URL:
// http://admin:secret@localhost:9200
var _ = Describe("opensearch trace kind (live)", func() {
	var address, index string
	var env tracestest.Env

	indexSpans := func(spans ...span) {
		GinkgoHelper()
		var lines []string
		for _, s := range spans {
			lines = append(lines,
				fmt.Sprintf(`{"index":{"_index":%q,"_id":%q}}`, index, s.id),
				fmt.Sprintf(`{"@timestamp":%q,"trace_id":%q,"span_id":%q,"service_name":%q,"duration_ms":4}`,
					s.at.UTC().Format(time.RFC3339Nano), s.traceID, s.id, s.service))
		}
		liveRequest(address, http.MethodPost, "/_bulk?refresh=true", strings.Join(lines, "\n")+"\n")
	}

	BeforeEach(func() {
		address = strings.TrimSpace(os.Getenv("COMMONS_DB_OPENSEARCH_URL"))
		if address == "" {
			Skip("COMMONS_DB_OPENSEARCH_URL is not set")
		}
		index = fmt.Sprintf("otel-traces-live-%d", time.Now().UnixNano())
		liveRequest(address, http.MethodPut, "/"+index, `{"mappings":{"properties":{
			"@timestamp":{"type":"date"},
			"trace_id":{"type":"keyword"},
			"span_id":{"type":"keyword"},
			"service_name":{"type":"keyword"},
			"duration_ms":{"type":"float"}
		}}}`)
		DeferCleanup(func() { liveRequest(address, http.MethodDelete, "/"+index, "") })

		now := time.Now()
		indexSpans(
			span{id: "s1", traceID: "t1", service: "checkout", at: now.Add(-10 * time.Minute)},
			span{id: "s2", traceID: "t1", service: "payments", at: now.Add(-9 * time.Minute)},
		)
		env = tracestest.NewEnv(map[string]traces.TracePlugin{"opensearch": opensearchtraces.Kind()})
	})

	start := func(params string) *query.Session {
		GinkgoHelper()
		ctx := dbcontext.New().WithConnectionResolver(func(_ dbcontext.Context, reference string) (*models.Connection, error) {
			switch reference {
			case "connection://traces":
				return &models.Connection{Name: "traces", Type: models.ConnectionTypeOpenTelemetry,
					Properties: types.JSONStringMap{"connection": "connection://spans"}}, nil
			case "connection://spans":
				return &models.Connection{Name: "spans", Type: models.ConnectionTypeOpenSearch, URL: address}, nil
			}
			return nil, nil
		})
		managed, err := env.Runtime.Start(ctx, traces.StartRequest{Kind: "opensearch", Params: json.RawMessage(
			fmt.Sprintf(`{"connection": "connection://traces", "index": %q, %s}`, index, params))})
		Expect(err).ToNot(HaveOccurred())
		return managed.Session()
	}

	sourceKeys := func(rows []recordstore.Row) []any {
		keys := []any{}
		for _, row := range rows {
			keys = append(keys, row["sourceKey"])
		}
		return keys
	}

	It("imports a window's spans and ends by itself", func() {
		info := tracestest.Ended(start(`"from": "now-1h"`))
		Expect(info.Error).To(BeEmpty())
		Expect(info.State).To(Equal(query.SessionCompleted))
		rows := env.Rows(info)
		Expect(rows).To(HaveLen(2))
		Expect(rows[0]["traceId"]).To(Equal("t1"))
		Expect(rows[0]["spanId"]).To(Equal("s1"))
		Expect(rows[0]["service"]).To(Equal("checkout"))
		Expect(rows[0]["sourceIndex"]).To(Equal(index))
		Expect(rows[1]["service"]).To(Equal("payments"))
		Expect(sourceKeys(rows)).To(ConsistOf(index+"/s1", index+"/s2"))
	})

	It("follows the index until stopped, storing each span once, including those indexed later", func() {
		session := start(`"from": "now-1h", "follow": true, "poll": "200ms"`)
		Eventually(func() int64 { return session.Snapshot().EventCount }).WithTimeout(30 * time.Second).Should(Equal(int64(2)))
		indexSpans(span{id: "s3", traceID: "t2", service: "checkout", at: time.Now()})
		Eventually(func() int64 { return session.Snapshot().EventCount }).WithTimeout(30 * time.Second).Should(Equal(int64(3)))
		session.Stop("spec")
		info := tracestest.Ended(session)
		Expect(info.State).To(Equal(query.SessionStopped))
		Expect(info.Error).To(BeEmpty())
		Expect(env.Sealed(info)).To(BeTrue())
		Expect(sourceKeys(env.Rows(info))).To(ConsistOf(index+"/s1", index+"/s2", index+"/s3"))
	})
})
