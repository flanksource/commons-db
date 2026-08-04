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

// traceConnections registers an OpenTelemetry connection whose nested
// OpenSearch connection points at address.
func traceConnections(address string) *gorm.DB {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	Expect(err).ToNot(HaveOccurred())
	Expect(database.Exec(`CREATE TABLE connections (
id TEXT PRIMARY KEY, name TEXT, namespace TEXT, source TEXT, type TEXT,
url TEXT, username TEXT, password TEXT, properties TEXT, certificate TEXT,
insecure_tls NUMERIC, created_at DATETIME, updated_at DATETIME, created_by TEXT
)`).Error).ToNot(HaveOccurred())
	Expect(database.Create(&models.Connection{
		ID: uuid.New(), Name: "OS", Type: models.ConnectionTypeOpenSearch, URL: address,
	}).Error).ToNot(HaveOccurred())
	Expect(database.Create(&models.Connection{
		ID: uuid.New(), Name: "traces", Type: models.ConnectionTypeOpenTelemetry,
		Properties: types.JSONStringMap{"connection": "connection://OS"},
	}).Error).ToNot(HaveOccurred())
	return database
}

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

		database := traceConnections(server.URL)
		result, err := query.Execute(context.New().WithDB(database, nil), query.Profile{
			Name: "jms",
			Provider: query.ProviderConfig{
				Type: "opentelemetry", Connection: "connection://traces",
				Options: map[string]any{
					"format": "jaeger", "index": "jaeger-span*", "dateField": "startTimeMillis",
					"traceIdField": "traceID", "spanIdField": "spanID", "serviceField": "process.serviceName",
					"operationField": "operationName", "statusFields": []string{"tag.otel@status_code"}, "selectFields": []string{"custom_field"},
					"search": map[string]any{"query": map[string]any{
						"op": "term", "field": "process.serviceName", "value": map[string]any{"param": "namespace"},
					}},
				},
			},
			Params:  []query.ParamDef{{Name: "namespace", Template: "{value}-api"}},
			Columns: []query.ColumnDef{{Name: "service"}},
		}, map[string]any{"namespace": "prod", "filter.service": "prod,!staging"})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(1))
		Expect(result.Rows[0]).To(HaveKeyWithValue("trace_id", "trace-1"))
		Expect(result.Rows[0]).To(HaveKeyWithValue("service", "prod-api"))
		Expect(result.Rows[0]).To(HaveKeyWithValue("input.xml", "<request/>"))
		Expect(result.Rows[0]).To(HaveKeyWithValue("duration_ms", float64(123)))
		Expect(result.Rows[0]).To(HaveKeyWithValue("custom_field", "from-fields"))

		boolQuery := requestBody["query"].(map[string]any)["bool"].(map[string]any)
		Expect(boolQuery["filter"]).To(ConsistOf(
			map[string]any{"term": map[string]any{"process.serviceName": "prod-api"}},
			map[string]any{"terms": map[string]any{"process.serviceName": []any{"prod"}}},
		))
		Expect(boolQuery["must_not"]).To(ConsistOf(
			map[string]any{"terms": map[string]any{"process.serviceName": []any{"staging"}}},
		))

		// The trace-shaped options fill what the specification left unset.
		Expect(requestBody["sort"]).To(Equal([]any{
			map[string]any{"startTimeMillis": map[string]any{"order": "desc"}},
		}))
		Expect(requestBody["stored_fields"]).To(Equal([]any{"*"}))
		Expect(requestBody["fields"]).To(Equal([]any{"custom_field"}))
	})

	It("lets the specification override a trace-shaped default", func() {
		var requestBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			Expect(json.NewDecoder(r.Body).Decode(&requestBody)).To(Succeed())
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`)
		}))
		defer server.Close()

		database := traceConnections(server.URL)
		profile := query.Profile{
			Name: "jms",
			Provider: query.ProviderConfig{
				Type: "opentelemetry", Connection: "connection://traces",
				Options: map[string]any{
					"format": "jaeger", "index": "jaeger-span*", "dateField": "startTimeMillis",
					"search": map[string]any{
						"sort":  []any{map[string]any{"field": "duration", "order": "asc"}},
						"query": map[string]any{"op": "match_all"},
					},
				},
			},
			Params: []query.ParamDef{{Name: "since", Role: query.ParamRoleTimeFrom}},
		}
		_, err := query.Execute(context.New().WithDB(database, nil), profile, map[string]any{"since": "now-2h"})
		Expect(err).ToNot(HaveOccurred())

		Expect(requestBody["sort"]).To(Equal([]any{
			map[string]any{"duration": map[string]any{"order": "asc"}},
		}))
		// dateField still seeds timeField, so a time-from param folds into a range.
		Expect(requestBody["query"]).To(Equal(map[string]any{"bool": map[string]any{
			"filter": []any{map[string]any{"range": map[string]any{
				"startTimeMillis": map[string]any{"gte": "now-2h"},
			}}},
		}}))
	})

	It("counts a param interpolated into the specification as referenced", func() {
		var requestBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			Expect(json.NewDecoder(r.Body).Decode(&requestBody)).To(Succeed())
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`)
		}))
		defer server.Close()

		database := traceConnections(server.URL)
		_, err := query.Execute(context.New().WithDB(database, nil), query.Profile{
			Name: "jms",
			Provider: query.ProviderConfig{
				Type: "opentelemetry", Connection: "connection://traces",
				Options: map[string]any{
					"format": "jaeger", "index": "jaeger-span*", "dateField": "startTimeMillis",
					"search": map[string]any{"query": map[string]any{
						"op": "term", "field": "process.serviceName", "value": "{{.params.country}}-api",
					}},
				},
			},
			Params: []query.ParamDef{{Name: "country", Default: "kenya"}},
		}, nil)
		Expect(err).ToNot(HaveOccurred())

		Expect(requestBody["query"]).To(Equal(
			map[string]any{"term": map[string]any{"process.serviceName": "kenya-api"}}))
	})
})

var _ = Describe("opensearch column filtering", func() {
	It("ORs the included values into one terms clause and excludes them via must_not", func() {
		var requestBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			Expect(json.NewDecoder(r.Body).Decode(&requestBody)).To(Succeed())
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"hits":{"total":{"value":0,"relation":"eq"},"hits":[]}}`)
		}))
		defer server.Close()

		_, err := query.Execute(context.New(), query.Profile{
			Name: "logs",
			Provider: query.ProviderConfig{Type: "opensearch", Options: map[string]any{
				"address": server.URL, "index": "logs-*",
			}},
			Query:   `{"query":{"match":{"message":"failed"}}}`,
			Columns: []query.ColumnDef{{Name: "service"}},
		}, map[string]any{"filter.service": "api,worker,!debug"})
		Expect(err).ToNot(HaveOccurred())

		outer := requestBody["query"].(map[string]any)["bool"].(map[string]any)
		Expect(outer["filter"]).To(ConsistOf(
			map[string]any{"match": map[string]any{"message": "failed"}},
			map[string]any{"terms": map[string]any{"service": []any{"api", "worker"}}},
		))
		Expect(outer["must_not"]).To(ConsistOf(
			map[string]any{"terms": map[string]any{"service": []any{"debug"}}},
		))
	})

	It("looks up distinct values with sibling filters but without the current column", func() {
		var requestBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			Expect(json.NewDecoder(r.Body).Decode(&requestBody)).To(Succeed())
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{
				"hits":{"total":{"value":0,"relation":"eq"},"hits":[]},
				"aggregations":{
					"__clicky_values":{"buckets":[{"key":"payments","doc_count":12}]},
					"__clicky_total":{"doc_count":12,"values":{"value":1}}
				}
			}`)
		}))
		defer server.Close()

		profile := query.Profile{
			Name: "logs",
			Provider: query.ProviderConfig{Type: "opensearch", Options: map[string]any{
				"address": server.URL, "index": "logs-*",
				"search": map[string]any{"query": map[string]any{"op": "match", "field": "message", "value": "failed"}},
			}},
			Columns: []query.ColumnDef{{Name: "service"}, {Name: "environment"}},
		}
		options, total, err := query.LookupFilterValues(
			context.New(), profile,
			map[string]any{"filter.service": "current", "filter.environment": "prod"},
			"filter.service", `pay+@#&<>~"`, 10,
		)
		Expect(err).ToNot(HaveOccurred())
		Expect(options).To(Equal([]query.FilterOption{{Value: "payments", Count: 12}}))
		Expect(total).To(Equal(1))

		aggregations := requestBody["aggregations"].(map[string]any)
		terms := aggregations["__clicky_values"].(map[string]any)["terms"].(map[string]any)
		Expect(terms).To(SatisfyAll(
			HaveKeyWithValue("field", "service"),
			HaveKeyWithValue("size", float64(10)),
			HaveKeyWithValue("include", `.*pay\+\@\#\&\<\>\~\".*`),
		))
		encoded, err := json.Marshal(requestBody["query"])
		Expect(err).ToNot(HaveOccurred())
		Expect(string(encoded)).To(ContainSubstring(`"environment":["prod"]`))
		Expect(string(encoded)).ToNot(ContainSubstring(`"service"`))
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
