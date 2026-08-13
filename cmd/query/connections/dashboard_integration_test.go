package connections

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

// newConnectionTestDB builds the connections table the dashboard and health
// handlers read, without needing a Postgres instance.
func newConnectionTestDB(connections []models.Connection) *gorm.DB {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	Expect(err).NotTo(HaveOccurred())
	Expect(database.Exec(`CREATE TABLE connections (
        id TEXT PRIMARY KEY, name TEXT, namespace TEXT, source TEXT, type TEXT,
        url TEXT, username TEXT, password TEXT, properties TEXT, certificate TEXT,
        insecure_tls NUMERIC, created_at DATETIME, updated_at DATETIME, created_by TEXT
    )`).Error).To(Succeed())
	if len(connections) > 0 {
		Expect(database.Create(&connections).Error).To(Succeed())
	}
	return database
}

// blackholeAddress returns an address that completes a TCP handshake and then
// never replies, which is how an unreachable-but-listening backend stalls a
// probe until its timeout.
func blackholeAddress() string {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			DeferCleanup(func() { _ = conn.Close() })
		}
	}()
	return listener.Addr().String()
}

var _ = Describe("connection dashboard endpoint", func() {
	It("returns one safe, profile-aware batch filtered like the connection list", func() {
		database := newConnectionTestDB([]models.Connection{
			{
				ID: uuid.New(), Name: "api", Namespace: "acme", Type: models.ConnectionTypeHTTP,
				URL: "https://operator:hunter2@example.test/api", InsecureTLS: true,
			},
			{ID: uuid.New(), Name: "cluster", Namespace: "acme", Type: models.ConnectionTypeKubernetes},
		})

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
		Expect(connection.Health).To(BeNil(), "listing must not probe")
		Expect(strings.Join([]string{
			connection.Endpoint.Host,
			connection.Endpoint.Path,
		}, "")).NotTo(ContainSubstring("hunter2"))
	})

	It("lists a fleet of stalling connections without waiting on any of them", func() {
		var connections []models.Connection
		for range 12 {
			connections = append(connections, models.Connection{
				ID: uuid.New(), Name: uuid.NewString(), Namespace: "acme",
				Type: models.ConnectionTypePostgres,
				URL:  "postgres://operator:hunter2@" + blackholeAddress() + "/app",
			})
		}
		database := newConnectionTestDB(connections)
		handler := newConnectionDashboardHandler(connectionDashboardHandlerOptions{
			Prefix:   "/api/v1",
			Context:  dbcontext.NewContext(context.Background()).WithDB(database, nil),
			Profiles: func(context.Context) ([]query.Profile, error) { return nil, nil },
			Next:     http.NotFoundHandler(),
		})

		recorder := httptest.NewRecorder()
		started := time.Now()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, connectionDashboardPath, nil))
		elapsed := time.Since(started)

		Expect(recorder.Code).To(Equal(http.StatusOK), recorder.Body.String())
		// Probing these 12 at the old concurrency of 6 cost two 5s rounds.
		Expect(elapsed).To(BeNumerically("<", 500*time.Millisecond))
		var response connectionDashboardResponse
		Expect(json.Unmarshal(recorder.Body.Bytes(), &response)).To(Succeed())
		Expect(response.Connections).To(HaveLen(12))
		for _, connection := range response.Connections {
			Expect(connection.Health).To(BeNil())
		}
	})

	It("surfaces a health result on the next listing once one has been checked", func() {
		connection := models.Connection{
			ID: uuid.New(), Name: "cluster", Namespace: "acme", Type: models.ConnectionTypeKubernetes,
		}
		database := newConnectionTestDB([]models.Connection{connection})
		ctx := dbcontext.NewContext(context.Background()).WithDB(database, nil)
		DeferCleanup(func() { forgetConnectionHealth(connection.ID.String()) })

		dashboard := newConnectionDashboardHandler(connectionDashboardHandlerOptions{
			Prefix:   "/api/v1",
			Context:  ctx,
			Profiles: func(context.Context) ([]query.Profile, error) { return nil, nil },
			Next:     newConnectionHealthHandler("/api/v1", ctx, http.NotFoundHandler()),
		})

		health := httptest.NewRecorder()
		dashboard.ServeHTTP(health, httptest.NewRequest(
			http.MethodPost, connectionHealthPath,
			strings.NewReader(`{"ids":["`+connection.ID.String()+`"]}`),
		))
		Expect(health.Code).To(Equal(http.StatusOK), health.Body.String())

		listing := httptest.NewRecorder()
		dashboard.ServeHTTP(listing, httptest.NewRequest(http.MethodGet, connectionDashboardPath, nil))

		var response connectionDashboardResponse
		Expect(json.Unmarshal(listing.Body.Bytes(), &response)).To(Succeed())
		Expect(response.Connections).To(HaveLen(1))
		Expect(response.Connections[0].Health).NotTo(BeNil())
		Expect(response.Connections[0].Health.State).To(Equal(connectionHealthUnverifiable))
		Expect(response.Connections[0].Health.Cached).To(BeTrue())
	})
})
