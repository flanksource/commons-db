package connections

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/commons-db/models"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("connection form lookup HTTP", func() {
	It("serves scoped searched namespaced references without exposing connection credentials", func() {
		service := newConflictTestService(newConnectionTestDB([]models.Connection{
			{ID: uuid.New(), Name: "warehouse", Namespace: "tenant", Type: "postgres", Password: "private-value"},
			{ID: uuid.New(), Name: "metrics", Type: "prometheus"},
		}))
		response := httptest.NewRecorder()
		service.Handler("/api/v1", http.NotFoundHandler()).ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/connection?__lookup=filters&__lookup_filter=connection&types=postgres&__lookup_q=ware", nil))
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		var result entity.LookupResponse
		Expect(json.Unmarshal(response.Body.Bytes(), &result)).To(Succeed())
		Expect(result.Filters["connection"].Options).To(Equal(map[string]entity.LookupNode{
			"connection://tenant/warehouse": {Kind: "text", Plain: "warehouse (postgres)", Text: "warehouse (postgres)"},
		}))
		Expect(response.Body.String()).NotTo(ContainSubstring("private-value"))
	})
})
