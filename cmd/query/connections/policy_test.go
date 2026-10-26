package connections

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/cmd/query/secretcatalog"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// sqlPolicy is an app that offers SQL connections whose credentials come from
// the cluster, and whose URLs may also be literal.
var sqlPolicy = Policy{
	AllowedTypes:  []string{models.ConnectionTypePostgres, models.ConnectionTypeSQLite},
	SecretSources: []string{SourceSecret, SourceConfigMap, SourceHelm},
	URLSources:    []string{SourceValue, SourceSecret},
}

func serveConnections(service *Service, method, path, accept string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	recorder := httptest.NewRecorder()
	service.Handler("/api/v1", http.NotFoundHandler()).ServeHTTP(recorder, request)
	return recorder
}

var _ = Describe("a connection policy", func() {
	var (
		layers  layeredStore
		service *Service
	)
	BeforeEach(func() {
		layers = newLayeredStore()
		writeConnectionFile(layers.app, "orders.yaml", "spec: {postgres: {url: {value: 'postgres://db/orders'}}}\n")
		writeConnectionFile(layers.app, "logs.yaml", "spec: {loki: {url: 'http://loki'}}\n")
		service = newStoreService(newAppStoreProvider(layers, nil), sqlPolicy)
	})

	DescribeTable("refuses a save outside the policy",
		func(body map[string]any, code, message string) {
			_, err := service.Create(context.Background(), body)
			expectStatus(err, http.StatusBadRequest, code)
			Expect(err).To(MatchError(ContainSubstring(message)))
			Expect(filepath.Join(layers.app, body["name"].(string)+".yaml")).NotTo(BeAnExistingFile())
		},
		Entry("a type it does not allow",
			map[string]any{"name": "cache", "type": models.ConnectionTypeRedis, "url": "redis://cache"}, "invalid_type", `type "redis"`),
		Entry("a literal password",
			map[string]any{"name": "billing", "type": models.ConnectionTypePostgres, "url": "postgres://db", "password": "hunter2-example"}, "literal_secret", "password is a literal value; it must come from secret, configmap, helm"),
		Entry("a literal username",
			map[string]any{"name": "billing", "type": models.ConnectionTypePostgres, "url": "postgres://db", "username": "app"}, "literal_secret", "username"),
		Entry("a password from a source it does not allow",
			map[string]any{"name": "billing", "type": models.ConnectionTypePostgres, "url": "postgres://db", "password": "op://vault/item/password"}, "literal_secret", "onepassword"),
		Entry("a url from a source it does not allow",
			map[string]any{"name": "billing", "type": models.ConnectionTypePostgres, "url": "configmap://db/url"}, "literal_secret", "url is a configmap reference"),
		Entry("a malformed reference",
			map[string]any{"name": "billing", "type": models.ConnectionTypePostgres, "url": "postgres://db", "password": "secret://only-a-name"}, "invalid_reference", "password"),
	)

	It("accepts credentials from the sources it allows and a literal url", func() {
		_, err := service.Create(context.Background(), map[string]any{
			"name": "billing", "type": models.ConnectionTypePostgres, "url": "postgres://$(username):$(password)@db/billing",
			"username": "configmap://db/user", "password": "secret://db/password",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(layers.find("billing").Connection.Password).To(Equal("secret://db/password"))
	})

	It("lists, looks up and dashboards only the allowed types", func() {
		listed, err := service.List(ListOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(HaveExactElements(HaveField("Name", "orders")))
		Expect(service.List(ListOptions{Type: models.ConnectionTypeLoki})).To(BeEmpty())

		options, total := connectionFilter{service: service}.OptionsWithQuery(ListOptions{}, "", 0)
		Expect([]any{total, options}).To(HaveExactElements(1, HaveKey("connection://orders")))

		recorder := serveConnections(service, http.MethodGet, "/api/v1/connections/dashboard", "")
		Expect(recorder.Code).To(Equal(http.StatusOK))
		var dashboard struct {
			Connections []struct{ Name string } `json:"connections"`
		}
		Expect(json.Unmarshal(recorder.Body.Bytes(), &dashboard)).To(Succeed())
		Expect(dashboard.Connections).To(HaveExactElements(HaveField("Name", "orders")))
	})

	It("serves the form schema narrowed to the policy", func() {
		recorder := serveConnections(service, http.MethodGet, "/api/v1/connections", profiles.SchemaContentType)
		Expect(recorder.Code).To(Equal(http.StatusOK))
		var document map[string]any
		Expect(json.Unmarshal(recorder.Body.Bytes(), &document)).To(Succeed())
		Expect(document["properties"].(map[string]any)["type"].(map[string]any)["enum"]).
			To(Equal([]any{models.ConnectionTypePostgres, models.ConnectionTypeSQLite}))
		Expect(document["x-clicky-secret-sources"]).To(Equal([]any{SourceSecret, SourceConfigMap, SourceHelm}))
		Expect(document["x-clicky-url-sources"]).To(Equal([]any{SourceValue, SourceSecret}))
	})

	It("serves the committed form schema unchanged without a policy", func() {
		open := newStoreService(newAppStoreProvider(layers, nil), Policy{})
		recorder := serveConnections(open, http.MethodGet, "/api/v1/connections", profiles.SchemaContentType)
		committed, err := os.ReadFile(filepath.Join("..", "..", "..", "schemas", "connection.json"))
		Expect(err).NotTo(HaveOccurred())
		Expect(append(recorder.Body.Bytes(), '\n')).To(Equal(committed))
		Expect(open.List(ListOptions{})).To(HaveLen(2))
	})

	DescribeTable("refuses a policy naming what it does not know",
		func(policy Policy, message string) {
			_, err := New(Options{
				Context:    func() dbcontext.Context { return dbcontext.New() },
				DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
				Profiles:   func(context.Context) ([]query.Profile, error) { return nil, nil },
				Virtual:    newAppStoreProvider(layers, nil),
				Policy:     policy,
			})
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("a type", Policy{AllowedTypes: []string{"postgresql"}}, `unknown connection type "postgresql"`),
		Entry("a secret source", Policy{SecretSources: []string{"vault"}}, `unknown value source "vault"`),
		Entry("a url source", Policy{URLSources: []string{"literal"}}, `unknown value source "literal"`),
	)
})

var _ = Describe("the connection form's secret pickers", func() {
	newService := func(secrets *secretcatalog.Options) *Service {
		service, err := New(Options{
			Context:    func() dbcontext.Context { return dbcontext.New() },
			DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
			Profiles:   func(context.Context) ([]query.Profile, error) { return nil, nil },
			Virtual:    newAppStoreProvider(newLayeredStore(), nil),
			Secrets:    secrets,
		})
		Expect(err).NotTo(HaveOccurred())
		return service
	}
	cluster := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop"}, Data: map[string][]byte{"password": []byte("example-value")}},
	)
	secrets := &secretcatalog.Options{Kube: func() (kubernetes.Interface, error) { return cluster, nil }}

	DescribeTable("answer under {prefix}/connections",
		func(path, expected string) {
			recorder := serveConnections(newService(secrets), http.MethodGet, path, "")
			Expect(recorder.Code).To(Equal(http.StatusOK), recorder.Body.String())
			Expect(recorder.Body.String()).To(MatchJSON(expected))
		},
		Entry("namespaces", "/api/v1/connections/namespaces", `["shop"]`),
		Entry("secrets", "/api/v1/connections/secrets?kind=secret&namespace=shop", `[{"name":"db","keys":["password"]}]`),
		Entry("a masked preview", "/api/v1/connections/secrets/preview?kind=secret&name=db&namespace=shop", `[{"key":"password","value":"exam••••alue"}]`),
	)

	It("leave the dashboard and schema to the service", func() {
		service := newService(secrets)
		Expect(serveConnections(service, http.MethodGet, "/api/v1/connections/dashboard", "").Code).To(Equal(http.StatusOK))
		Expect(serveConnections(service, http.MethodGet, "/api/v1/connections", profiles.SchemaContentType).Code).To(Equal(http.StatusOK))
	})

	It("are not served unless the host asks for them", func() {
		Expect(serveConnections(newService(nil), http.MethodGet, "/api/v1/connections/namespaces", "").Code).To(Equal(http.StatusNotFound))
	})
})
