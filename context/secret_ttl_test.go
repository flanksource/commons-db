package context

import (
	gocontext "context"
	"time"

	"github.com/flanksource/commons/logger"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"

	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons-db/models"
)

var _ = ginkgo.Describe("WithSecretTTL", func() {
	const ref = "op://prod/postgres/password"
	var reads int

	ginkgo.BeforeEach(func() {
		ginkgo.GinkgoT().Setenv("OP_SERVICE_ACCOUNT_TOKEN", "")
		reads = 0
		envCache.Flush()
		previous := opReadFunc
		opReadFunc = func(_ Context, ref, _ string) (string, error) {
			reads++
			return "value of " + ref, nil
		}
		ginkgo.DeferCleanup(func() {
			opReadFunc = previous
			envCache.Flush()
		})
	})

	hydrate := func(ctx gocontext.Context) {
		ginkgo.GinkgoHelper()
		connection, err := HydrateConnection(NewContext(ctx), &models.Connection{Name: "lab", Password: ref})
		Expect(err).NotTo(HaveOccurred())
		Expect(connection.Password).To(Equal("value of " + ref))
	}

	ginkgo.It("keeps a value for the context's lifetime at 0", func() {
		ctx := New().WithSecretTTL(0)
		for range 3 {
			hydrate(ctx)
			envCache.Flush()
		}
		Expect(reads).To(Equal(1))
	})

	ginkgo.It("reads again once a value is older than the TTL", func() {
		ctx := New().WithSecretTTL(50 * time.Millisecond)
		hydrate(ctx)
		envCache.Flush()
		hydrate(ctx)
		Expect(reads).To(Equal(1))
		time.Sleep(60 * time.Millisecond)
		hydrate(ctx)
		Expect(reads).To(Equal(2))
	})

	ginkgo.It("does not serve a process-cached value older than the TTL", func() {
		hydrate(New())
		time.Sleep(20 * time.Millisecond)
		hydrate(New().WithSecretTTL(10 * time.Millisecond))
		Expect(reads).To(Equal(2))
	})

	ginkgo.It("shares values across contexts derived at the same TTL only", func() {
		ctx := New().WithSecretTTL(0)
		hydrate(ctx)
		envCache.Flush()
		hydrate(ctx.WithSecretTTL(0))
		hydrate(ctx.Wrap(gocontext.Background()))
		Expect(reads).To(Equal(1))
		hydrate(ctx.WithSecretTTL(time.Minute))
		Expect(reads).To(Equal(2))
		envCache.Flush()
		hydrate(New().WithSecretTTL(0))
		Expect(reads).To(Equal(3))
	})

	ginkgo.It("refuses a negative TTL", func() {
		Expect(func() { New().WithSecretTTL(-1) }).To(PanicWith(ContainSubstring("negative")))
	})

	ginkgo.It("keeps a Kubernetes secret until it is invalidated", func() {
		ctx := cacheTestContext("https://cluster", "principal", "old", "endpoint").WithSecretTTL(0)
		read := func() (string, error) {
			envCache.Flush()
			return GetSecretFromCache(ctx, "prod", "database", "password")
		}
		Expect(read()).To(Equal("old"))
		client, err := ctx.LocalKubernetes()
		Expect(err).NotTo(HaveOccurred())
		secret, err := client.CoreV1().Secrets("prod").Get(ctx, "database", metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		secret.Data["password"] = []byte("new")
		_, err = client.CoreV1().Secrets("prod").Update(ctx, secret, metav1.UpdateOptions{})
		Expect(err).NotTo(HaveOccurred())

		Expect(read()).To(Equal("old"))
		Expect(InvalidateSecretCache(ctx, "prod", "database", "password")).To(Succeed())
		Expect(read()).To(Equal("new"))
	})

	ginkgo.DescribeTable("never keeps a service account token past its own expiry", func(validFor time.Duration, tokens int) {
		requests := 0
		clientset := fake.NewSimpleClientset()
		clientset.PrependReactor("create", "serviceaccounts", func(ktesting.Action) (bool, runtime.Object, error) {
			requests++
			return true, &authenticationv1.TokenRequest{Status: authenticationv1.TokenRequestStatus{
				Token: "token", ExpirationTimestamp: metav1.NewTime(time.Now().Add(validFor)),
			}}, nil
		})
		client := dutyKubernetes.NewKubeClient(logger.GetLogger("test"), clientset, &rest.Config{Host: "https://cluster", BearerToken: "principal"})
		ctx := New().WithLocalKubernetes(client).WithSecretTTL(0)
		for range 2 {
			Expect(GetServiceAccountTokenFromCache(ctx, "prod", "reader")).To(Equal("token"))
			envCache.Flush()
			time.Sleep(60 * time.Millisecond)
		}
		Expect(requests).To(Equal(tokens))
	},
		// A token is dropped a minute before it expires.
		ginkgo.Entry("expiring between reads", time.Minute+50*time.Millisecond, 2),
		ginkgo.Entry("still valid", time.Hour, 1),
	)
})
