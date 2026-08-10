package connections

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

var _ = Describe("connection dashboard endpoint", func() {
	It("returns one safe, profile-aware batch filtered like the connection list", func() {
		database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		Expect(err).NotTo(HaveOccurred())
		Expect(database.Exec(`CREATE TABLE connections (
            id TEXT PRIMARY KEY, name TEXT, namespace TEXT, source TEXT, type TEXT,
            url TEXT, username TEXT, password TEXT, properties TEXT, certificate TEXT,
            insecure_tls NUMERIC, created_at DATETIME, updated_at DATETIME, created_by TEXT
        )`).Error).To(Succeed())

		connections := []models.Connection{
			{
				ID: uuid.New(), Name: "api", Namespace: "acme", Type: models.ConnectionTypeHTTP,
				URL: "https://operator:hunter2@example.test/api", InsecureTLS: true,
			},
			{ID: uuid.New(), Name: "cluster", Namespace: "acme", Type: models.ConnectionTypeKubernetes},
		}
		Expect(database.Create(&connections).Error).To(Succeed())

		handler := newConnectionDashboardHandler(connectionDashboardHandlerOptions{
			Prefix:  "/api/v1",
			Context: dbcontext.NewContext(context.Background()).WithDB(database, nil),
			Profiles: func(context.Context) ([]query.Profile, error) {
				return []query.Profile{{
					Name: "status", Provider: query.ProviderConfig{Connection: "connection://api"},
				}}, nil
			},
			Next: http.NotFoundHandler(),
		})
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(
			recorder,
			httptest.NewRequest(http.MethodGet, connectionDashboardPath+"?type=http", nil),
		)

		Expect(recorder.Code).To(Equal(http.StatusOK), recorder.Body.String())
		Expect(recorder.Body.String()).NotTo(ContainSubstring("hunter2"))
		var response connectionDashboardResponse
		Expect(json.Unmarshal(recorder.Body.Bytes(), &response)).To(Succeed())
		Expect(response.Connections).To(HaveLen(1))
		connection := response.Connections[0]
		Expect(connection.Name).To(Equal("api"))
		Expect(connection.Namespace).To(Equal("acme"))
		Expect(connection.ProfileCount).To(Equal(1))
		Expect(connection.InlineCredential).To(BeTrue())
		Expect(connection.InsecureTLS).To(BeTrue())
		Expect(connection.Health.State).To(Equal(connectionHealthUnverifiable))
		Expect(strings.Join([]string{
			connection.Endpoint.Host,
			connection.Endpoint.Path,
		}, "")).NotTo(ContainSubstring("hunter2"))
	})
})
