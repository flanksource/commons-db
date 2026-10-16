package connections

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("connection schema by content negotiation", func() {
	serve := func(method, path, accept string) (*httptest.ResponseRecorder, bool) {
		reached := false
		next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusTeapot)
		})
		request := httptest.NewRequest(method, path, nil)
		request.Header.Set("Accept", accept)
		recorder := httptest.NewRecorder()
		newConflictTestService(newConnectionTestDB(nil)).Handler("/api/v1", next).ServeHTTP(recorder, request)
		return recorder, reached
	}

	DescribeTable("serves the connection form schema for a schema Accept",
		func(path string) {
			recorder, reached := serve(http.MethodGet, path, profiles.SchemaContentType)
			Expect(reached).To(BeFalse())
			Expect(recorder.Code).To(Equal(http.StatusOK))
			Expect(recorder.Header().Get("Content-Type")).To(Equal(profiles.SchemaContentType))
			var document map[string]any
			Expect(json.Unmarshal(recorder.Body.Bytes(), &document)).To(Succeed())
			Expect(document).To(HaveKey("$schema"))
			Expect(recorder.Body.String()).To(ContainSubstring(`"redis"`))
		},
		Entry("singular", "/api/v1/connection"),
		Entry("plural with a trailing slash", "/api/v1/connections/"),
	)

	DescribeTable("delegates everything else",
		func(method, path, accept string) {
			recorder, reached := serve(method, path, accept)
			Expect(reached).To(BeTrue())
			Expect(recorder.Code).To(Equal(http.StatusTeapot))
		},
		Entry("a data Accept", http.MethodGet, "/api/v1/connection", "application/json"),
		Entry("a single connection", http.MethodGet, "/api/v1/connection/warehouse", profiles.SchemaContentType),
		Entry("an unsafe method", http.MethodPost, "/api/v1/connection", profiles.SchemaContentType),
	)
})
