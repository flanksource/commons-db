package profiles

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/otel/span/layout"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
	"github.com/flanksource/commons-db/query/providers"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Canonical query translation endpoint", func() {
	It("imports native conditions and resolves another layout without searching or changing logical conditions", func() {
		for _, mode := range []string{"import", "export", "empty"} {
			native := mode != "export"
			fields := map[string]string{"trace_id": "keyword", "span_id": "keyword", "timestamp": "date", "duration_ms": "double", "service": "keyword"}
			if native {
				fields = map[string]string{"traceID": "keyword", "spanID": "keyword", "startTimeMillis": "long", "duration": "long", "process.serviceName": "keyword"}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer GinkgoRecover()
				if r.Method == http.MethodHead {
					return
				}
				if strings.Contains(r.URL.Path, "/_mapping/field/") {
					w.Header().Set("Content-Type", "application/json")
					Expect(json.NewEncoder(w).Encode(map[string]any{"spans": map[string]any{"mappings": map[string]any{"timestamp": map[string]any{"full_name": "timestamp", "mapping": map[string]any{"timestamp": map[string]any{"type": "date"}}}}}})).To(Succeed())
					return
				}
				Expect(strings.HasSuffix(r.URL.Path, "_field_caps")).To(BeTrue(), r.URL.Path)
				caps := map[string]any{}
				for name, typ := range fields {
					caps[name] = map[string]any{typ: map[string]any{"searchable": true, "aggregatable": true}}
				}
				w.Header().Set("Content-Type", "application/json")
				Expect(json.NewEncoder(w).Encode(map[string]any{"fields": caps})).To(Succeed())
			}))
			DeferCleanup(server.Close)
			profile := query.Profile{Provider: query.ProviderConfig{Type: "opensearch", Options: map[string]any{"address": server.URL, "index": "spans"}}}
			direction := layout.ToCanonical
			body := json.RawMessage(`{"size":25,"query":{"range":{"duration":{"gte":200}}}}`)
			if mode == "empty" {
				body = nil
			}
			if !native {
				direction = layout.ToSource
				body = nil
				size := 25
				profile.Search = &esdsl.Search{Size: &size, Query: &esdsl.Condition{Op: esdsl.OpRange, Field: "duration_ms", Gte: esdsl.Literal(0.2)}}
			}
			encoded, err := json.Marshal(providers.SpanQueryTranslation{Profile: profile, Direction: direction, Query: body})
			Expect(err).NotTo(HaveOccurred())
			handler := newProfileSampleHandler("/api/v1", dbcontext.New(), nil, &recordingHandler{})
			response := post(handler, "/api/v1/profile/sample/query", string(encoded))
			Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
			var result providers.TranslatedSpanQuery
			Expect(json.Unmarshal(response.Body.Bytes(), &result)).To(Succeed())
			if mode == "empty" {
				Expect(result.Search.Query).To(BeNil())
				continue
			}
			Expect(result.Search.Query.Field).To(Equal("duration_ms"))
			Expect(result.Search.Size).NotTo(BeNil())
			Expect(*result.Search.Size).To(Equal(25))
			Expect(string(result.Canonical)).To(MatchJSON(`{"size":25,"query":{"range":{"duration_ms":{"gte":0.2}}}}`))
		}
	})
})
