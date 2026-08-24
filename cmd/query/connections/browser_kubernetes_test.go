package connections

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

var _ = Describe("Kubernetes connection browser", func() {
	It("uses a workload target and catalogs resources from the saved connection", func() {
		database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		Expect(err).NotTo(HaveOccurred())
		Expect(database.Exec(`CREATE TABLE connections (
            id TEXT PRIMARY KEY, name TEXT, namespace TEXT, source TEXT, type TEXT,
            url TEXT, username TEXT, password TEXT, properties TEXT, certificate TEXT,
            insecure_tls NUMERIC, created_at DATETIME, updated_at DATETIME, created_by TEXT
        )`).Error).NotTo(HaveOccurred())
		connection := models.Connection{
			ID: uuid.New(), Name: "cluster-a", Type: models.ConnectionTypeKubernetes,
			Certificate: "test kubeconfig",
		}
		Expect(database.Create(&connection).Error).NotTo(HaveOccurred())

		ctx := dbcontext.NewContext(context.Background()).WithDB(database, nil)
		handler := newConnectionBrowserHandler("/api/v1", ctx, http.NotFoundHandler())
		handler.kubernetesClient = func(_ context.Context, selected *models.Connection) (kubernetes.Interface, error) {
			Expect(selected.ID).To(Equal(connection.ID))
			return fake.NewSimpleClientset(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "payments"}},
				&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-abc12", Namespace: "payments"}},
				&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "node-agent", Namespace: "payments"}},
			), nil
		}
		baseURL := "/api/v1/connection/" + connection.ID.String() + "/browser"

		descriptorResponse := httptest.NewRecorder()
		handler.ServeHTTP(descriptorResponse, httptest.NewRequest(http.MethodGet, baseURL, nil))
		Expect(descriptorResponse.Code).To(Equal(http.StatusOK))
		var descriptor browserDescriptor
		Expect(json.Unmarshal(descriptorResponse.Body.Bytes(), &descriptor)).To(Succeed())
		Expect(descriptor.Target).To(Equal(&browserTarget{
			Kind: "kubernetes-workload", Label: "Workload",
			Kinds: []string{"pod", "deployment", "statefulset", "daemonset"},
		}))
		Expect(descriptor.AllowEmptyQuery).To(BeTrue())
		Expect(descriptor.InitialOptions).To(Equal(map[string]any{"limit": "200"}))

		namespaceResponse := httptest.NewRecorder()
		handler.ServeHTTP(namespaceResponse, httptest.NewRequest(http.MethodGet, baseURL+"/namespaces", nil))
		Expect(namespaceResponse.Code).To(Equal(http.StatusOK))
		Expect(namespaceResponse.Body.String()).To(MatchJSON(`["payments"]`))

		workloadResponse := httptest.NewRecorder()
		handler.ServeHTTP(workloadResponse, httptest.NewRequest(
			http.MethodGet,
			baseURL+"/workloads?namespace=payments&kinds=pod,daemonset",
			nil,
		))
		Expect(workloadResponse.Code).To(Equal(http.StatusOK))
		Expect(workloadResponse.Body.String()).To(MatchJSON(`{
			"pod":[{"name":"api-abc12"}],
			"daemonset":[{"name":"node-agent"}]
		}`))
	})
})
