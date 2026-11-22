package context

import (
	gocontext "context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flanksource/commons/logger"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/patrickmn/go-cache"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"

	dutyKubernetes "github.com/flanksource/commons-db/kubernetes"
	"github.com/flanksource/commons-db/models"
)

var _ = ginkgo.Describe("secret cache lookup", func() {
	var id string

	ginkgo.BeforeEach(func() { id = ginkgo.CurrentSpecReport().FullText() })
	ginkgo.AfterEach(func() { envCache.Delete(id) })

	ginkgo.It("rejects a token that expired before caching", func() {
		ctx := New().WithSecretTTL(0)
		lookup := envLookup{id: id, resolve: func(_ Context) (string, time.Time, error) {
			return "expired", time.Now().Add(-time.Second), nil
		}}
		value, err := lookup.get(ctx)
		Expect(err).To(HaveOccurred())
		Expect(value).To(BeEmpty())
		_, processCached := envCache.Get(id)
		_, scopeCached := secretScopeOf(ctx).values.Get(id)
		Expect([]bool{processCached, scopeCached}).To(Equal([]bool{false, false}))
	})

	ginkgo.DescribeTable("refreshes an expired token still present in the process cache", func(scoped bool) {
		ctx := New()
		if scoped {
			ctx = ctx.WithSecretTTL(0)
		}
		now := time.Now()
		envCache.Set(id, envValue{value: "expired", resolved: now, expires: now.Add(-time.Second)}, cache.NoExpiration)
		lookup := envLookup{id: id, resolve: func(_ Context) (string, time.Time, error) {
			return "fresh", now.Add(time.Hour), nil
		}}
		Expect(lookup.get(ctx)).To(Equal("fresh"))
		didResolve := false
		lookup.resolve = func(_ Context) (string, time.Time, error) {
			didResolve = true
			return "unexpected", time.Time{}, nil
		}
		Expect(lookup.get(ctx)).To(Equal("fresh"))
		Expect(didResolve).To(BeFalse())
	},
		ginkgo.Entry("process only", false),
		ginkgo.Entry("with a scope", true),
	)

	ginkgo.DescribeTable("lets a caller leave while a shared lookup continues", func(cancelOwner, scopedOwner bool) {
		base := New()
		ctx := base.WithSecretTTL(0)
		ownerContext := base
		if scopedOwner {
			ownerContext = ctx
		}
		owner, cancelOwnerContext := ownerContext.WithCancel()
		ginkgo.DeferCleanup(cancelOwnerContext)
		started := make(chan struct{})
		markStarted := sync.OnceFunc(func() { close(started) })
		release := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()

		var resolutions atomic.Int32
		lookup := envLookup{id: id, resolve: func(resolver Context) (string, time.Time, error) {
			resolutions.Add(1)
			markStarted()
			<-release
			if err := resolver.Err(); err != nil {
				return "", time.Time{}, err
			}
			return "shared", time.Time{}, nil
		}}
		ownerResult := make(chan error, 1)
		go func() {
			_, err := lookup.get(owner)
			ownerResult <- err
		}()
		Eventually(started).Should(BeClosed())

		if cancelOwner {
			cancelOwnerContext()
			Eventually(ownerResult).Should(Receive(MatchError(gocontext.Canceled)))
		} else {
			waiter, cancelWaiter := ctx.WithTimeout(50 * time.Millisecond)
			defer cancelWaiter()
			waiterResult := make(chan error, 1)
			go func() {
				_, err := lookup.get(waiter)
				waiterResult <- err
			}()
			Eventually(waiterResult).Should(Receive(MatchError(gocontext.DeadlineExceeded)))
		}

		unblock()
		Expect(lookup.get(ctx)).To(Equal("shared"))
		Expect(resolutions.Load()).To(Equal(int32(1)))
		if !cancelOwner {
			Eventually(ownerResult).Should(Receive(Succeed()))
		}
	},
		ginkgo.Entry("joining caller's deadline", false, true),
		ginkgo.Entry("scoped initiating caller's cancellation", true, true),
		ginkgo.Entry("unscoped initiating caller's cancellation", true, false),
	)

	ginkgo.DescribeTable("cannot restore a value resolved before invalidation", func(scoped bool) {
		ctx := New()
		if scoped {
			ctx = ctx.WithSecretTTL(0)
		}
		started := make(chan struct{})
		release := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		old := envLookup{id: id, ttl: time.Minute, resolve: func(_ Context) (string, time.Time, error) {
			close(started)
			<-release
			return "old", time.Time{}, nil
		}}
		oldResult := make(chan error, 1)
		go func() {
			_, err := old.get(ctx)
			oldResult <- err
		}()
		Eventually(started).Should(BeClosed())
		dropEnvValue(ctx, id)

		fresh := envLookup{id: id, ttl: time.Minute, resolve: func(_ Context) (string, time.Time, error) {
			return "fresh", time.Time{}, nil
		}}
		type result struct {
			value string
			err   error
		}
		nextResult := make(chan result, 1)
		go func() {
			value, err := fresh.get(ctx)
			nextResult <- result{value: value, err: err}
		}()
		Eventually(nextResult).Should(Receive(Equal(result{value: "fresh"})))
		unblock()
		Eventually(oldResult).Should(Receive(Succeed()))
		Expect(fresh.get(ctx)).To(Equal("fresh"))
		Expect(fresh.get(New())).To(Equal("fresh"))
	},
		ginkgo.Entry("process only", false),
		ginkgo.Entry("with a scope", true),
	)
})

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
