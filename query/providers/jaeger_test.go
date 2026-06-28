package providers_test

import (
	"net/http"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// jaegerPayload mirrors the shape of Jaeger's GET /api/traces response: a `data`
// array of traces, each carrying its own `spans` and a `processes` map keyed by
// the span's processID (which holds the serviceName and resource tags).
const jaegerPayload = `{
  "data": [
    {
      "traceID": "abc123",
      "spans": [
        {"traceID":"abc123","spanID":"span-1","operationName":"GET /policy","startTime":1780410800000000,"duration":15000,"processID":"p1","tags":[{"key":"http.status_code","value":200}]},
        {"traceID":"abc123","spanID":"span-2","operationName":"SELECT AsPolicy","startTime":1780410800010000,"duration":8200,"processID":"p2","tags":[{"key":"db.statement","value":"SELECT 1"}]}
      ],
      "processes": {
        "p1": {"serviceName":"policy-api","tags":[{"key":"k8s.pod.name","value":"policy-api-7d9"}]},
        "p2": {"serviceName":"app-db","tags":[{"key":"k8s.pod.name","value":"mssql-0"}]}
      }
    }
  ]
}`

var _ = Describe("jaeger provider", func() {
	profile := func(url string) query.Profile {
		return query.Profile{
			Name: "jaeger spans",
			Provider: query.ProviderConfig{
				Type:    "jaeger",
				Options: map[string]any{"url": url, "service": "policy-api"},
			},
		}
	}

	It("flattens trace spans into one row per span with merged process tags", func() {
		srv := jsonServer(http.StatusOK, jaegerPayload)
		defer srv.Close()

		result, err := query.Execute(context.New(), profile(srv.URL))
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(2))

		first := result.Rows[0]
		Expect(first).To(HaveKeyWithValue("spanID", "span-1"))
		Expect(first).To(HaveKeyWithValue("operationName", "GET /policy"))
		Expect(first).To(HaveKeyWithValue("serviceName", "policy-api"))

		tags, ok := first["tags"].(map[string]any)
		Expect(ok).To(BeTrue(), "tags should flatten the {key,value} arrays into a map")
		Expect(tags).To(HaveKeyWithValue("k8s.pod.name", "policy-api-7d9"))
		Expect(tags).To(HaveKeyWithValue("http.status_code", BeNumerically("==", 200)))
	})

	It("maps spans into canonical LogsTable columns via CEL", func() {
		srv := jsonServer(http.StatusOK, jaegerPayload)
		defer srv.Close()

		p := profile(srv.URL)
		p.Columns = []query.ColumnDef{
			{Name: "message", CEL: "row.operationName"},
			{Name: "logger", CEL: "row.serviceName"},
			{Name: "pod", CEL: "row.tags['k8s.pod.name']"},
		}

		result, err := query.Execute(context.New(), p)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows[1]).To(HaveKeyWithValue("message", "SELECT AsPolicy"))
		Expect(result.Rows[1]).To(HaveKeyWithValue("logger", "app-db"))
		Expect(result.Rows[1]).To(HaveKeyWithValue("pod", "mssql-0"))
	})

	It("evaluates the jaeger-traces profile's guarded CEL mappings", func() {
		srv := jsonServer(http.StatusOK, jaegerPayload)
		defer srv.Close()

		p := profile(srv.URL)
		p.Render = query.RenderLogs
		p.Columns = []query.ColumnDef{
			{Name: "timestamp", Type: query.ColumnTypeDateTime, CEL: "row.startTime"},
			{Name: "level", Type: query.ColumnTypeStatus, CEL: "'error' in row.tags ? 'error' : 'info'"},
			{Name: "pod", CEL: "'k8s.pod.name' in row.tags ? row.tags['k8s.pod.name'] : ''"},
			{Name: "duration", Type: query.ColumnTypeDuration, CEL: "row.duration"},
		}

		result, err := query.Execute(context.New(), p)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows[0]).To(HaveKeyWithValue("level", "info"))
		Expect(result.Rows[0]).To(HaveKeyWithValue("pod", "policy-api-7d9"))
		Expect(result.Rows[0]).To(HaveKey("timestamp"))
		Expect(result.Rows[0]).To(HaveKey("duration"))
	})

	It("fails loudly when neither a trace id nor a service is supplied", func() {
		srv := jsonServer(http.StatusOK, jaegerPayload)
		defer srv.Close()

		p := query.Profile{
			Name:     "jaeger no-filter",
			Provider: query.ProviderConfig{Type: "jaeger", Options: map[string]any{"url": srv.URL}},
		}
		_, err := query.Execute(context.New(), p)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("service"))
	})
})
