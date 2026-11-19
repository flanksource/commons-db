package lease

import (
	"context"
	"database/sql"
	"os"
	"time"

	"github.com/google/uuid"
	_ "github.com/microsoft/go-mssqldb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/valkey-io/valkey-go"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var _ = Describe("live lease backends", Label("integration"), func() {
	for _, engine := range []struct{ name, env string }{{"Redis", "COMMONS_DB_LEASE_REDIS_URL"}, {"Valkey", "COMMONS_DB_LEASE_VALKEY_URL"}} {
		lockerContract(engine.name, allModes, func(options ExpiringOptions) (Locker, error) {
			client := newTestRedis(engine.env)
			return NewRedis(client, options)
		})
	}

	lockerContract("SQL Server", allModes, func(options ExpiringOptions) (Locker, error) {
		db, err := sql.Open("sqlserver", testSetting("COMMONS_DB_LEASE_SQLSERVER_URL"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		return NewSQLServer(db, options.Options)
	})

	lockerContract("Kubernetes API server", allModes, func(options ExpiringOptions) (Locker, error) {
		config, err := clientcmd.BuildConfigFromFlags("", testSetting("COMMONS_DB_LEASE_KUBECONFIG"))
		Expect(err).NotTo(HaveOccurred())
		client, err := kubernetes.NewForConfig(config)
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		name := "lease-" + options.Scope
		namespaces := client.CoreV1().Namespaces()
		_, err = namespaces.Create(ctx, &core.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.CreateOptions{})
		if !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func(ctx SpecContext) error { return namespaces.Delete(ctx, name, metav1.DeleteOptions{}) })
		}
		return NewKubernetes(client.CoordinationV1().Leases(name), options)
	})

	DescribeTable("rejects stale writers after expiry", func(ctx SpecContext, env string) {
		client := newTestRedis(env)
		backend := newRedisBackend(client, 3*time.Second)
		adapter, err := NewRedis(client, ExpiringOptions{Options: Options{Scope: "expiry-test"}, TTL: backend.ttl})
		Expect(err).NotTo(HaveOccurred())
		key := uuid.NewString()
		stale, err := backend.begin(request{scope: "expiry-test", key: key, holder: "stale", mode: writeMode}, false).try(ctx)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func(ctx SpecContext) error { return stale.unlock(ctx) })
		var next Lease
		Eventually(func(g Gomega) bool {
			held, ok, err := adapter.Mutex(key, as("next")).TryLock(ctx)
			g.Expect(err).NotTo(HaveOccurred())
			next = held
			return ok
		}, 5*time.Second, 50*time.Millisecond).Should(BeTrue())
		DeferCleanup(next.Unlock, ctx)
		Expect(stale.renew(ctx)).To(MatchError(errNotOwned))
		Expect(stale.unlock(ctx)).To(Succeed())
		expectBusy(adapter.Mutex(key, as("contender")).TryLock(ctx))
	}, Entry("Redis", "COMMONS_DB_LEASE_REDIS_URL"), Entry("Valkey", "COMMONS_DB_LEASE_VALKEY_URL"))
})

func testSetting(name string) string {
	value := os.Getenv(name)
	if value == "" {
		Skip("set " + name + " to a disposable test service")
	}
	return value
}

func newTestRedis(env string) valkey.Client {
	options, err := valkey.ParseURL(testSetting(env))
	Expect(err).NotTo(HaveOccurred())
	options.DisableCache = true
	client, err := valkey.NewClient(options)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(client.Close)
	return client
}
