package profiles

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("renamed profile column exports", func() {
	It("uses only the new field name in JSON and CSV", func() {
		query.RegisterProvider(&execStreamMock{rows: []query.Row{{"request_count": 12, "service": "payments"}}})
		store, err := NewFileStore(GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		Expect(store.Save(context.Background(), query.Profile{
			Name: "export", Provider: query.ProviderConfig{Type: "exec-stream"}, Query: "rows",
			Columns: []query.ColumnDef{
				{Name: "requests", Source: "request_count", Type: query.ColumnTypeNumber},
				{Name: "service"},
			},
		})).To(Succeed())
		handler := newExecHandler("/api/v1", dbcontext.New(), store, &nextMarker{})

		jsonResponse := get(handler, "/api/v1/profile/export?format=json", "")
		Expect(jsonResponse.Code).To(Equal(200), jsonResponse.Body.String())
		var rows []map[string]any
		Expect(json.Unmarshal(jsonResponse.Body.Bytes(), &rows)).To(Succeed())
		Expect(rows).To(Equal([]map[string]any{{"requests": float64(12), "service": "payments"}}))

		csvResponse := get(handler, "/api/v1/profile/export?format=csv", "")
		Expect(csvResponse.Code).To(Equal(200), csvResponse.Body.String())
		records, err := csv.NewReader(strings.NewReader(csvResponse.Body.String())).ReadAll()
		Expect(err).ToNot(HaveOccurred())
		Expect(records).To(Equal([][]string{{"Requests", "Service"}, {"12.00", "payments"}}))

		ndjsonResponse := get(handler, "/api/v1/profile/export?format=ndjson", "")
		Expect(ndjsonResponse.Code).To(Equal(200), ndjsonResponse.Body.String())
		var row map[string]any
		Expect(json.Unmarshal([]byte(strings.TrimSpace(ndjsonResponse.Body.String())), &row)).To(Succeed())
		Expect(row).To(Equal(map[string]any{"requests": float64(12), "service": "payments"}))
	})
})
