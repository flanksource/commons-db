package connections

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/flanksource/clicky/entity"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// configCatalog is a host whose whole connection catalog is its own
// configuration: every connection is virtual and there is no store.
type configCatalog struct {
	connections []*models.Connection
	err         error
}

func (c configCatalog) ListConnections() ([]*models.Connection, error) { return c.connections, c.err }

func (c configCatalog) ResolveConnection(_ dbcontext.Context, id string) (*models.Connection, error) {
	for _, connection := range c.connections {
		if id == connection.Name || id == connection.ID.String() {
			return connection, nil
		}
	}
	return nil, nil
}

func newVirtualOnlyService(catalog configCatalog) *Service {
	ctx := dbcontext.NewContext(context.Background())
	service, err := New(Options{
		Context:    func() dbcontext.Context { return ctx },
		DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
		Profiles:   func(context.Context) ([]query.Profile, error) { return nil, nil },
		Virtual:    catalog,
	})
	Expect(err).NotTo(HaveOccurred())
	return service
}

func expectReadOnly(err error) {
	var status *entity.StatusError
	Expect(errors.As(err, &status)).To(BeTrue(), "expected a read-only StatusError, got %v", err)
	Expect([]any{status.Status, status.Code}).To(Equal([]any{http.StatusMethodNotAllowed, "read_only"}))
}

var _ = Describe("connections without a store", func() {
	var (
		service *Service
		backend *httptest.Server
		catalog configCatalog
	)

	BeforeEach(func() {
		backend = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		DeferCleanup(backend.Close)
		catalog = configCatalog{connections: []*models.Connection{
			{ID: uuid.New(), Name: "devops", Type: models.ConnectionTypeHTTP, URL: backend.URL, Virtual: true, ReadOnly: true},
			{ID: uuid.New(), Name: "archive", Type: models.ConnectionTypeFolder, Virtual: true},
		}}
		service = newVirtualOnlyService(catalog)
		DeferCleanup(func() { forgetConnectionHealth(catalog.connections[0].ID.String()) })
	})

	serve := func(method, path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		service.Handler("/api/v1", http.NotFoundHandler()).
			ServeHTTP(recorder, httptest.NewRequest(method, path, strings.NewReader(body)))
		return recorder
	}

	It("lists and gets only the virtual connections", func() {
		listed, err := service.List(ListOptions{Type: models.ConnectionTypeHTTP})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(HaveLen(1))
		Expect([]any{listed[0].Name, listed[0].URL}).To(Equal([]any{"devops", ""}), "a virtual URL is redacted")

		got, err := service.Get("archive")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.ID).To(Equal(catalog.connections[1].ID))
		Expect(catalog.connections[0].URL).To(Equal(backend.URL), "redaction must not mutate the host's catalog")
	})

	It("refuses create, update and delete with 405 read_only", func() {
		body := map[string]any{"name": "new", "type": models.ConnectionTypeHTTP}
		_, err := service.Create(context.Background(), body)
		expectReadOnly(err)
		_, err = service.Update(context.Background(), "devops", body)
		expectReadOnly(err)
		expectReadOnly(service.Delete("archive"))
		expectReadOnly(service.Delete("missing"))
	})

	It("serves the dashboard from the virtual catalog, flagged read-only", func() {
		recorder := serve(http.MethodGet, "/api/v1/connections/dashboard", "")

		Expect(recorder.Code).To(Equal(http.StatusOK), recorder.Body.String())
		var response connectionDashboardResponse
		Expect(json.Unmarshal(recorder.Body.Bytes(), &response)).To(Succeed())
		Expect(response.ReadOnly).To(BeTrue())
		var rows []string
		for _, item := range response.Connections {
			Expect(item.ReadOnly).To(BeTrue(), item.Name)
			rows = append(rows, item.Name)
		}
		Expect(rows).To(Equal([]string{"archive", "devops"}))
	})

	It("probes a virtual connection in the health batch", func() {
		id := catalog.connections[0].ID.String()
		recorder := serve(http.MethodPost, "/api/v1/connections/health", `{"ids":["`+id+`"],"force":true}`)

		Expect(recorder.Code).To(Equal(http.StatusOK), recorder.Body.String())
		var response connectionHealthResponse
		Expect(json.Unmarshal(recorder.Body.Bytes(), &response)).To(Succeed())
		Expect(response.Results).To(HaveLen(1))
		Expect([]any{response.Results[0].ID, response.Results[0].State}).
			To(Equal([]any{id, connectionHealthHealthy}), response.Results[0].Detail)
	})

	It("tests a read-only connection as stored, since its listed URL is redacted", func() {
		id := catalog.connections[0].ID.String()
		recorder := serve(http.MethodPost, "/api/v1/connection/test",
			`{"id":"`+id+`","name":"devops","type":"`+models.ConnectionTypeHTTP+`","url":""}`)

		Expect(recorder.Code).To(Equal(http.StatusOK), recorder.Body.String())
		var result testResult
		Expect(json.Unmarshal(recorder.Body.Bytes(), &result)).To(Succeed())
		Expect(result.OK).To(BeTrue(), result.Message)
	})

	It("fails list and dashboard when the host cannot read its catalog, not lists it empty", func() {
		catalog.err = errors.New("parse global config: yaml: line 3")
		service = newVirtualOnlyService(catalog)

		_, err := service.List(ListOptions{})
		Expect(err).To(MatchError(ContainSubstring("parse global config")))
		recorder := serve(http.MethodGet, "/api/v1/connections/dashboard", "")
		Expect(recorder.Code).To(Equal(http.StatusInternalServerError))
		Expect(recorder.Body.String()).To(ContainSubstring("parse global config"))
	})

	It("fails the health batch fast on an id no catalog knows", func() {
		recorder := serve(http.MethodPost, "/api/v1/connections/health", `{"ids":["`+uuid.NewString()+`"]}`)

		Expect(recorder.Code).To(Equal(http.StatusBadRequest))
		Expect(recorder.Body.String()).To(ContainSubstring("unknown connection ids"))
	})
})
