package connections

import (
	"context"
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/clicky/rpc"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type upstreamRequest struct {
	Method, Path, RawQuery, Authorization, Cookie string
}

var _ = Describe("connection proxy", func() {
	const (
		basicUser     = "picker"
		basicPassword = "s3cret"
		bearerToken   = "upstream-token"
	)
	var (
		received []upstreamRequest
		upstream *httptest.Server
		catalog  configCatalog
	)

	BeforeEach(func() {
		received = nil
		upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			received = append(received, upstreamRequest{
				Method: r.Method, Path: r.URL.EscapedPath(), RawQuery: r.URL.RawQuery,
				Authorization: r.Header.Get("Authorization"), Cookie: r.Header.Get("Cookie"),
			})
			http.SetCookie(w, &http.Cookie{Name: "upstream-session", Value: "leaked"})
			w.WriteHeader(http.StatusTeapot)
		}))
		DeferCleanup(upstream.Close)
		catalog = configCatalog{connections: []*models.Connection{
			{ID: uuid.New(), Name: "basic", Type: models.ConnectionTypeHTTP, URL: upstream.URL + "/base", Username: basicUser, Password: basicPassword, Virtual: true},
			{ID: uuid.New(), Name: "bearer", Type: models.ConnectionTypeHTTP, URL: upstream.URL, Properties: map[string]string{"bearer": bearerToken}, Virtual: true},
			{ID: uuid.New(), Name: "warehouse", Type: models.ConnectionTypePostgres, URL: "postgres://warehouse.example/db", Virtual: true},
			{ID: uuid.New(), Name: "down", Type: models.ConnectionTypeHTTP, URL: "http://127.0.0.1:1", Virtual: true},
		}}
	})

	serve := func(service *Service, method, target string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, target, nil)
		request.Header.Set("Authorization", "Bearer caller-token")
		request.Header.Set("Cookie", "session=caller")
		recorder := httptest.NewRecorder()
		service.Handler("/api/v1", http.NotFoundHandler()).ServeHTTP(recorder, request)
		return recorder
	}

	It("injects the connection's basic auth, strips caller credentials and joins path and query", func() {
		recorder := serve(newVirtualOnlyService(catalog), http.MethodGet, "/api/v1/connections/basic/proxy/api/v1/plan?limit=1&q=a%20b")

		Expect(recorder.Code).To(Equal(http.StatusTeapot), recorder.Body.String())
		probe := httptest.NewRequest(http.MethodGet, "/", nil)
		probe.SetBasicAuth(basicUser, basicPassword)
		Expect(received).To(Equal([]upstreamRequest{{
			Method: http.MethodGet, Path: "/base/api/v1/plan", RawQuery: "limit=1&q=a%20b",
			Authorization: probe.Header.Get("Authorization"),
		}}))
	})

	It("drops the upstream's Set-Cookie so its session never reaches the caller's origin", func() {
		recorder := serve(newVirtualOnlyService(catalog), http.MethodGet, "/api/v1/connections/bearer/proxy/login")

		Expect(recorder.Code).To(Equal(http.StatusTeapot), recorder.Body.String())
		Expect(received).To(HaveLen(1), "the upstream must have answered with its cookie")
		Expect(recorder.Header().Values("Set-Cookie")).To(BeEmpty())
	})

	It("injects the connection's bearer token on HEAD", func() {
		recorder := serve(newVirtualOnlyService(catalog), http.MethodHead, "/api/v1/connections/bearer/proxy/health")

		Expect(recorder.Code).To(Equal(http.StatusTeapot), recorder.Body.String())
		Expect(received).To(Equal([]upstreamRequest{{Method: http.MethodHead, Path: "/health", Authorization: "Bearer " + bearerToken}}))
	})

	DescribeTable("refuses without reaching the upstream",
		func(method, target string, status int) {
			recorder := serve(newVirtualOnlyService(catalog), method, target)

			Expect(recorder.Code).To(Equal(status), recorder.Body.String())
			Expect(received).To(BeEmpty())
		},
		Entry("a write method", http.MethodPost, "/api/v1/connections/basic/proxy/api", http.StatusMethodNotAllowed),
		Entry("an unknown connection", http.MethodGet, "/api/v1/connections/missing/proxy/api", http.StatusNotFound),
		Entry("a non-http connection", http.MethodGet, "/api/v1/connections/warehouse/proxy/api", http.StatusBadRequest),
		Entry("a path escaping the connection's base", http.MethodGet, "/api/v1/connections/basic/proxy/../admin", http.StatusBadRequest),
	)

	It("reports an unreachable upstream as 502 naming the connection", func() {
		recorder := serve(newVirtualOnlyService(catalog), http.MethodGet, "/api/v1/connections/down/proxy/api")

		Expect(recorder.Code).To(Equal(http.StatusBadGateway))
		Expect(recorder.Body.String()).To(ContainSubstring(`connection "down"`))
	})

	It("hides an http connection the policy does not allow", func() {
		ctx := dbcontext.NewContext(context.Background())
		service, err := New(Options{
			Context:    func() dbcontext.Context { return ctx },
			DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
			Profiles:   func(context.Context) ([]query.Profile, error) { return nil, nil },
			Virtual:    catalog,
			Policy:     Policy{AllowedTypes: []string{models.ConnectionTypePostgres}},
		})
		Expect(err).NotTo(HaveOccurred())

		Expect(serve(service, http.MethodGet, "/api/v1/connections/basic/proxy/api").Code).To(Equal(http.StatusNotFound))
		Expect(received).To(BeEmpty())
	})

	It("documents GET and HEAD in OpenAPI", func() {
		spec := &rpc.OpenAPISpec{Paths: map[string]rpc.OpenAPIPath{}}
		AddConnectionsOpenAPI(spec)

		Expect(spec.Paths[connectionProxyOpenAPIPath]).To(HaveKey("get"))
		Expect(spec.Paths[connectionProxyOpenAPIPath]).To(HaveKey("head"))
		Expect(spec.Paths[connectionProxyOpenAPIPath]).NotTo(HaveKey("post"))
	})
})
