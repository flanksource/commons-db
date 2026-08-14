package profiles

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("streamable log processors", func() {
	DescribeTable("applies logs.parse before writing the export",
		func(scope, expectedMode string) {
			provider := &execStreamMock{rows: []query.Row{
				{"id": 1, "message": `{"level":"error","msg":"request failed","request_id":"req-41"}`},
				{"id": 2, "message": `{"level":"info","msg":"request complete","request_id":"req-42"}`},
			}}
			query.RegisterProvider(provider)

			store, err := NewFileStore(GinkgoT().TempDir())
			Expect(err).ToNot(HaveOccurred())
			Expect(store.Save(context.Background(), query.Profile{
				Name:       "structured-logs",
				Provider:   query.ProviderConfig{Type: provider.Type()},
				Query:      "rows",
				Order:      query.Order{{Column: "id", Unique: true}},
				Processors: []query.ProcessorSpec{{Use: "logs.json"}},
			})).To(Succeed())

			handler := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})
			request := httptest.NewRequest(http.MethodGet, "/api/v1/profile/structured-logs?format=json&limit=1&scope="+scope, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
			Expect(response.Header().Get("X-Export-Mode")).To(Equal(expectedMode))
			var rows []query.Row
			Expect(json.Unmarshal(response.Body.Bytes(), &rows)).To(Succeed())
			Expect(rows[0]).To(SatisfyAll(
				HaveKeyWithValue("message", "request failed"),
				HaveKeyWithValue("severity", "error"),
				HaveKeyWithValue("request_id", "req-41"),
			))
		},
		Entry("one page", "page", "page"),
		Entry("all rows", "all", "streaming"),
	)
})
