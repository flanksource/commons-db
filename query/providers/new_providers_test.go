package providers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var _ = Describe("opentelemetry provider", func() {
	It("queries Jaeger spans through its nested OpenSearch connection", func() {
		var requestBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			Expect(json.NewDecoder(r.Body).Decode(&requestBody)).To(Succeed())
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"hits":{"total":{"value":1,"relation":"eq"},"hits":[{"_id":"one","_source":{"traceID":"trace-1","spanID":"span-1","operationName":"process message","startTimeMillis":1710000000000,"duration":123000,"process":{"serviceName":"prod-api"},"tags":[{"key":"otel@status_code","value":"ERROR"},{"key":"input@xml","value":"<request/>"}]},"fields":{"custom_field":["from-fields"]}}]}}`)
		}))
		defer server.Close()

		database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		Expect(err).ToNot(HaveOccurred())
		Expect(database.Exec(`CREATE TABLE connections (
id TEXT PRIMARY KEY, name TEXT, namespace TEXT, source TEXT, type TEXT,
url TEXT, username TEXT, password TEXT, properties TEXT, certificate TEXT,
insecure_tls NUMERIC, created_at DATETIME, updated_at DATETIME, created_by TEXT
)`).Error).ToNot(HaveOccurred())
		Expect(database.Create(&models.Connection{
			ID: uuid.New(), Name: "OS", Type: models.ConnectionTypeOpenSearch, URL: server.URL,
		}).Error).ToNot(HaveOccurred())
		Expect(database.Create(&models.Connection{
			ID: uuid.New(), Name: "traces", Type: models.ConnectionTypeOpenTelemetry,
			Properties: types.JSONStringMap{"connection": "connection://OS"},
		}).Error).ToNot(HaveOccurred())

		result, err := query.Execute(context.New().WithDB(database, nil), query.Profile{
			Name: "jms",
			Provider: query.ProviderConfig{
				Type: "opentelemetry", Connection: "connection://traces",
				Options: map[string]any{
					"format": "jaeger", "index": "jaeger-span*", "dateField": "startTimeMillis",
					"traceIdField": "traceID", "spanIdField": "spanID", "serviceField": "process.serviceName",
					"operationField": "operationName", "statusFields": []string{"tag.otel@status_code"}, "selectFields": []string{"custom_field"},
					"params": map[string]any{"namespace": map[string]any{"field": "process.serviceName", "operator": "term"}},
				},
			},
			Params: []query.ParamDef{{Name: "namespace", Template: "{value}-api"}},
		}, map[string]any{"namespace": "prod"})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(1))
		Expect(result.Rows[0]).To(HaveKeyWithValue("trace_id", "trace-1"))
		Expect(result.Rows[0]).To(HaveKeyWithValue("service", "prod-api"))
		Expect(result.Rows[0]).To(HaveKeyWithValue("input.xml", "<request/>"))
		Expect(result.Rows[0]).To(HaveKeyWithValue("duration_ms", float64(123)))
		Expect(result.Rows[0]).To(HaveKeyWithValue("custom_field", "from-fields"))

		boolQuery := requestBody["query"].(map[string]any)["bool"].(map[string]any)
		filter := boolQuery["filter"].([]any)
		Expect(filter[0]).To(Equal(map[string]any{"term": map[string]any{"process.serviceName": "prod-api"}}))
	})
})

var _ = Describe("postgrest provider", func() {
	It("returns rows from a PostgREST JSON array response", func() {
		srv := jsonServer(http.StatusOK, `[{"id":1,"name":"alpha"},{"id":2,"name":"beta"}]`)
		defer srv.Close()

		result, err := query.Execute(context.New(), query.Profile{
			Name:     "pgrst",
			Provider: query.ProviderConfig{Type: "postgrest"},
			Query:    srv.URL, // full URL acts as the resource endpoint
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(2))
		Expect(result.Rows[0]).To(HaveKeyWithValue("name", "alpha"))
	})
})

var _ = Describe("loki provider", func() {
	const lokiResponse = `{"status":"success","data":{"resultType":"streams","result":[` +
		`{"stream":{"app":"checkout"},"values":[["1700000000000000000","payment failed"]]}]}}`

	It("returns one row per log line with labels", func() {
		srv := jsonServer(http.StatusOK, lokiResponse)
		defer srv.Close()

		result, err := query.Execute(context.New(), query.Profile{
			Name: "loki",
			Provider: query.ProviderConfig{
				Type:    "loki",
				Options: map[string]any{"url": srv.URL},
			},
			Query: `{app="checkout"}`,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(1))
		Expect(result.Rows[0]).To(HaveKeyWithValue("message", "payment failed"))
		Expect(result.Rows[0]).To(HaveKeyWithValue("app", "checkout"))
	})
})

var _ = Describe("clickhouse provider registration", func() {
	It("registers per-engine SQL aliases", func() {
		for _, typ := range []string{"sql", "postgres", "mysql", "sqlserver", "clickhouse"} {
			_, err := query.GetProvider(typ)
			Expect(err).ToNot(HaveOccurred(), "provider %q should be registered", typ)
		}
	})
})
