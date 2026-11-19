package providers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/otel/span/layout"
	"github.com/flanksource/commons-db/query/providers"
	"github.com/flanksource/commons-db/types"
)

// tagsAsFieldsCatalog is the _field_caps of a Jaeger index written with
// tags_as_fields.
var tagsAsFieldsCatalog = map[string]string{
	"traceID": "keyword", "spanID": "keyword", "startTimeMillis": "long", "duration": "long",
	"operationName": "keyword", "process": "object", "process.serviceName": "keyword",
	"tag": "object", "process.tag": "object", "tag.http@method": "keyword",
}

var _ = Describe("ResolveSpanLayout", func() {
	It("discovers a connection index's tag fields and applies the connection span_layout over them", func() {
		inspected := []string{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer GinkgoRecover()
			if r.Method == http.MethodHead {
				return
			}
			Expect(r.URL.Path).To(HaveSuffix("/_field_caps"))
			inspected = append(inspected, strings.TrimSuffix(r.URL.Path, "/_field_caps"))
			fields := map[string]any{}
			for name, typ := range tagsAsFieldsCatalog {
				fields[name] = map[string]any{typ: map[string]any{"searchable": true, "aggregatable": true}}
			}
			w.Header().Set("Content-Type", "application/json")
			Expect(json.NewEncoder(w).Encode(map[string]any{"fields": fields})).To(Succeed())
		}))
		DeferCleanup(server.Close)
		ctx := dbcontext.New().WithConnectionResolver(func(_ dbcontext.Context, reference string) (*models.Connection, error) {
			switch reference {
			case "connection://traces":
				return &models.Connection{Name: "traces", Type: models.ConnectionTypeOpenTelemetry, Properties: types.JSONStringMap{
					"connection": "connection://spans", "index": "jaeger-span-*",
					"span_layout": `{"flattened":[{"prefix":"resource.","fieldPrefix":"process.tag.","separator":"_"}]}`,
				}}, nil
			case "connection://spans":
				return &models.Connection{Name: "spans", Type: models.ConnectionTypeOpenSearch, URL: server.URL}, nil
			}
			return nil, nil
		})

		definition, err := providers.ResolveSpanLayout(ctx, providers.SpanLayoutRequest{Connection: "connection://traces"})
		Expect(err).NotTo(HaveOccurred())
		Expect(inspected).To(Equal([]string{"/jaeger-span-*"}))
		Expect(definition.Fields["timestamp"]).To(Equal(layout.Field{Source: "startTimeMillis", Type: "long", Unit: "ms"}))
		Expect(definition.Flattened).To(ConsistOf(
			layout.FlattenedAttributes{Prefix: "attributes.", FieldPrefix: "tag.", Separator: "@"},
			layout.FlattenedAttributes{Prefix: "resource.", FieldPrefix: "process.tag.", Separator: "_"},
		))
	})
})
