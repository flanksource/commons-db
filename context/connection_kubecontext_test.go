package context_test

import (
	gocontext "context"
	"fmt"

	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	dbcontext "github.com/flanksource/commons-db/context"
	dbkubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons-db/models"
)

var _ = Describe("hydrating a connection that names a kubeconfig context", func() {
	// cluster is a fake cluster whose secret, configmap and helm-free values
	// carry the cluster's own name, so a value shows which client read it.
	cluster := func(name string) *dbkubernetes.Client {
		clientset := fake.NewSimpleClientset(
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop"}, Data: map[string][]byte{"password": []byte(name + "-password")}},
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop"}, Data: map[string]string{"user": name + "-user"}},
		)
		return dbkubernetes.NewKubeClient(logger.GetLogger("test"), clientset, &rest.Config{Host: "https://" + name + ".example.test"})
	}
	clusters := map[string]*dbkubernetes.Client{"staging": cluster("staging"), "production": cluster("production")}

	var requested []string
	var ctx dbcontext.Context
	BeforeEach(func() {
		requested = nil
		ctx = dbcontext.NewContext(gocontext.Background()).
			WithLocalKubernetes(cluster("ambient")).
			WithKubeContextClients(func(kubeContext string) (*dbkubernetes.Client, error) {
				requested = append(requested, kubeContext)
				if client, ok := clusters[kubeContext]; ok {
					return client, nil
				}
				return nil, fmt.Errorf("kubeconfig has no context %q", kubeContext)
			})
	})

	connection := func(kubeContext string) *models.Connection {
		properties := map[string]string{}
		if kubeContext != "" {
			properties["kubecontext"] = kubeContext
		}
		return &models.Connection{
			Name: "orders", Namespace: "shop", Type: models.ConnectionTypePostgres,
			URL:      "postgres://$(username):$(password)@db/orders",
			Username: "configmap://db/user", Password: "secret://db/password",
			Properties: properties,
		}
	}

	DescribeTable("reads its references through that context's cluster",
		func(kubeContext, expectedURL string) {
			hydrated, err := dbcontext.HydrateConnection(ctx, connection(kubeContext))
			Expect(err).ToNot(HaveOccurred())
			Expect(hydrated.URL).To(Equal(expectedURL))
		},
		Entry("staging", "staging", "postgres://staging-user:staging-password@db/orders"),
		Entry("production", "production", "postgres://production-user:production-password@db/orders"),
		Entry("no kubecontext: the context's own client", "", "postgres://ambient-user:ambient-password@db/orders"),
	)

	It("asks for the client once per hydration and only when the property is set", func() {
		_, err := dbcontext.HydrateConnection(ctx, connection(""))
		Expect(err).ToNot(HaveOccurred())
		Expect(requested).To(BeEmpty())
		_, err = dbcontext.HydrateConnection(ctx, connection("staging"))
		Expect(err).ToNot(HaveOccurred())
		Expect(requested).To(Equal([]string{"staging"}))
	})

	It("fails, naming the connection and context, when the context has no client", func() {
		_, err := dbcontext.HydrateConnection(ctx, connection("missing"))
		Expect(err).To(MatchError(And(ContainSubstring(`connection "orders"`), ContainSubstring(`kubeconfig has no context "missing"`))))
	})

	It("keeps the client factory across Wrap", func() {
		hydrated, err := dbcontext.HydrateConnection(ctx.Wrap(gocontext.Background()), connection("production"))
		Expect(err).ToNot(HaveOccurred())
		Expect(hydrated.Password).To(Equal("production-password"))
	})
})
