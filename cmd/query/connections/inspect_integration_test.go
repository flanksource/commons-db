package connections

import (
	"context"
	"net/http"
	"net/http/httptest"

	dbcontext "github.com/flanksource/commons-db/context"
	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons/logger"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

const inspectionSecretNamespace = "observability"

// openSearchInspectionStub answers the requests a target enumeration makes: the
// client's ping, the index resolution loadTargets reads, and the field caps a
// selected target resolves.
func openSearchInspectionStub() *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/_resolve/index/*":
			_, _ = w.Write([]byte(`{"indices":[{"name":"logs-2","aliases":["logs"],"attributes":[]}],` +
				`"aliases":[{"name":"logs","indices":["logs-2"]}],"data_streams":[]}`))
		case "/logs/_field_caps":
			_, _ = w.Write([]byte(`{"fields":{"service.name":{"keyword":{"searchable":true,"aggregatable":true}}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	DeferCleanup(server.Close)
	return server
}

// inspectionSecretContext scopes every spec to its own cluster fingerprint. The
// env cache is a package global keyed off the rest config, so a shared host
// would let one spec's resolved value satisfy another spec's lookup.
func inspectionSecretContext(database *gorm.DB, secret map[string][]byte) dbcontext.Context {
	clientset := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "os-credentials", Namespace: inspectionSecretNamespace},
		Data:       secret,
	})
	client := dutyKubernetes.NewKubeClient(logger.GetLogger("test"), clientset, &rest.Config{
		Host: "https://" + uuid.NewString(), BearerToken: uuid.NewString(),
	})
	return dbcontext.NewContext(context.Background()).WithDB(database, nil).WithLocalKubernetes(client)
}

func newInspectionService(ctx dbcontext.Context, database *gorm.DB) *Service {
	service, err := New(Options{
		Database:   func() (*gorm.DB, error) { return database, nil },
		Context:    func() dbcontext.Context { return ctx },
		DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
		Profiles:   func(context.Context) ([]query.Profile, error) { return nil, nil },
	})
	Expect(err).ToNot(HaveOccurred())
	return service
}

// secretBackedConnection is the shape the k8s-url-selector widget persists: the
// stored row carries secret:// references, never addresses or credentials.
func secretBackedConnection() models.Connection {
	return models.Connection{
		ID: uuid.New(), Name: "OM Non-Prod", Namespace: inspectionSecretNamespace,
		Type: models.ConnectionTypeOpenSearch, URL: "secret://os-credentials/url",
		Properties: map[string]string{
			"authType": "basic", "username": "reader", "password": "secret://os-credentials/password",
		},
	}
}

var _ = Describe("connection inspection against a secret-backed connection", func() {
	var (
		connection models.Connection
		service    *Service
	)

	BeforeEach(func() {
		server := openSearchInspectionStub()
		connection = secretBackedConnection()
		database := newConnectionTestDB([]models.Connection{connection})
		service = newInspectionService(inspectionSecretContext(database, map[string][]byte{
			"url": []byte(server.URL), "password": []byte("hunter2"),
		}), database)
	})

	It("resolves a secret:// url into an address instead of dialing the reference", func() {
		result, err := service.Inspect(context.Background(), connection.ID.String(), InspectFlags{})

		Expect(err).ToNot(HaveOccurred())
		names := make([]string, 0, len(result.Targets))
		for _, target := range result.Targets {
			names = append(names, target.Target)
		}
		Expect(names).To(ContainElement("logs"))
	})

	It("resolves the secret before enumerating a selected target's field catalog", func() {
		result, err := service.Inspect(context.Background(), connection.ID.String(), InspectFlags{
			Target: "logs", TargetKind: "alias",
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Result).ToNot(BeNil())
		Expect(result.Result.Fields).To(HaveLen(1))
		Expect(result.Result.Fields[0].Name).To(Equal("service.name"))
	})

	It("reports an unresolvable reference as a secret failure, not a protocol scheme", func() {
		unresolvable := secretBackedConnection()
		database := newConnectionTestDB([]models.Connection{unresolvable})
		service := newInspectionService(inspectionSecretContext(database, map[string][]byte{
			"password": []byte("hunter2"),
		}), database)

		_, err := service.Inspect(context.Background(), unresolvable.ID.String(), InspectFlags{})

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("os-credentials"))
		Expect(err.Error()).ToNot(ContainSubstring("unsupported protocol scheme"))
	})
})
