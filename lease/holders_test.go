package lease

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	api "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttest "k8s.io/client-go/testing"
)

func as(name string) LockOptions { return LockOptions{Holder: name} }

// holderBackend describes how a backend reports holders: lose ends an
// ownership without unlocking it, and namesGroups says whether a group
// holder carries its group name.
type holderBackend struct {
	factory     func(ExpiringOptions) (Locker, error)
	lose        func(context.Context, ownership)
	namesGroups bool
}

func holdersContract(name string, backend holderBackend) {
	Describe(name+" holders", func() {
		var first, second Locker
		BeforeEach(func() {
			options := ExpiringOptions{Options: Options{Scope: uuid.NewString(), RetryInterval: 10 * time.Millisecond}, TTL: contractTTL}
			var err error
			first, err = backend.factory(options)
			Expect(err).NotTo(HaveOccurred())
			second, err = backend.factory(options)
			Expect(err).NotTo(HaveOccurred())
		})

		It("reports the writer whose lease made a TryLock fail", func(ctx SpecContext) {
			before := time.Now().Add(-time.Second)
			hold(ctx, first.Mutex("deploy", as("alpha")))
			contender := second.Mutex("deploy", as("beta"))
			expectBusy(contender.TryLock(ctx))
			Expect(holdersOf(ctx, contender, before)).To(Equal([]Holder{{Name: "alpha", Mode: "write"}}))
		}, SpecTimeout(8*time.Second))

		It("lists every reader and every group holder by name", func(ctx SpecContext) {
			before := time.Now().Add(-time.Second)
			for _, reader := range []ReadWriteLock{first.ReadWriteLock("schema", as("reader-1")), second.ReadWriteLock("schema", as("reader-2"))} {
				held, err := reader.RLock(ctx)
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(held.Unlock, ctx)
			}
			Expect(holdersOf(ctx, first.Mutex("schema", as("probe")), before)).To(ConsistOf(
				Holder{Name: "reader-1", Mode: "read"}, Holder{Name: "reader-2", Mode: "read"}))

			hold(ctx, first.GroupLock("country", "USA", as("usa-1")))
			hold(ctx, second.GroupLock("country", "USA", as("usa-2")))
			group := "USA"
			if !backend.namesGroups {
				group = ""
			}
			Expect(holdersOf(ctx, second.GroupLock("country", "RSA", as("rsa")), before)).To(ConsistOf(
				Holder{Name: "usa-1", Mode: "group", Group: group}, Holder{Name: "usa-2", Mode: "group", Group: group}))
		}, SpecTimeout(8*time.Second))

		It("reports no holder after unlock", func(ctx SpecContext) {
			held := hold(ctx, first.Mutex("deploy", as("alpha")))
			Expect(held.Unlock(ctx)).To(Succeed())
			Expect(second.Mutex("deploy", as("beta")).Holders(ctx)).To(BeEmpty())
		}, SpecTimeout(8*time.Second))

		It("drops a holder whose ownership is lost", func(ctx SpecContext) {
			probe := second.Mutex("deploy", as("beta"))
			req, err := first.Mutex("deploy", as("stale")).(lock).request()
			Expect(err).NotTo(HaveOccurred())
			stale, err := first.(*locker).begin(req, false).try(ctx)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func(ctx SpecContext) error { return stale.unlock(ctx) })
			Expect(probe.Holders(ctx)).To(ConsistOf(HaveField("Name", "stale")))
			backend.lose(ctx, stale)
			Eventually(func(g Gomega) {
				holders, err := probe.Holders(ctx)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(holders).To(BeEmpty())
			}, 3*contractTTL, 100*time.Millisecond).Should(Succeed())
		}, SpecTimeout(15*time.Second))
	})
}

// holdersOf lists the holders of lock, checks each Since lies between
// before and now, and clears it so the caller can compare the rest.
func holdersOf(ctx context.Context, lock Lock, before time.Time) []Holder {
	holders, err := lock.Holders(ctx)
	Expect(err).NotTo(HaveOccurred())
	for i := range holders {
		Expect(holders[i].Since).To(BeTemporally(">=", before))
		Expect(holders[i].Since).To(BeTemporally("<=", time.Now().Add(time.Second)))
		holders[i].Since = time.Time{}
	}
	return holders
}

// expireUnrenewed relies on the backend's TTL to retire an ownership that is
// never renewed.
func expireUnrenewed(context.Context, ownership) {}

// newFakeLeases returns a fake Lease client that rejects stale resource
// versions, as the API server does.
func newFakeLeases() *fake.Clientset {
	client := fake.NewClientset()
	client.PrependReactor("update", "leases", func(action clienttest.Action) (bool, runtime.Object, error) {
		next := action.(clienttest.UpdateAction).GetObject().(*api.Lease).DeepCopy()
		stored, err := client.Tracker().Get(action.GetResource(), action.GetNamespace(), next.Name)
		if err != nil {
			return true, nil, err
		}
		current := stored.(*api.Lease).ResourceVersion
		if next.ResourceVersion != current {
			return true, nil, apierrors.NewConflict(action.GetResource().GroupResource(), next.Name, nil)
		}
		version, _ := strconv.Atoi(current)
		next.ResourceVersion = strconv.Itoa(version + 1)
		return true, next, client.Tracker().Update(action.GetResource(), next, action.GetNamespace())
	})
	return client
}

var _ = Describe("lease holders", func() {
	client := newFakeLeases()
	holdersContract("Kubernetes", holderBackend{namesGroups: true, lose: expireUnrenewed, factory: func(options ExpiringOptions) (Locker, error) {
		return NewKubernetes(client.CoordinationV1().Leases("lease-holders"), options)
	}})
})

var _ = Describe("live lease holders", Label("integration"), func() {
	for _, engine := range []struct{ name, env string }{{"Redis", "COMMONS_DB_LEASE_REDIS_URL"}, {"Valkey", "COMMONS_DB_LEASE_VALKEY_URL"}} {
		holdersContract(engine.name, holderBackend{namesGroups: true, lose: expireUnrenewed, factory: func(options ExpiringOptions) (Locker, error) {
			return NewRedis(newTestRedis(engine.env), options)
		}})
	}

	holdersContract("SQL Server", holderBackend{
		lose: func(_ context.Context, held ownership) {
			Expect(discardSQLConnection(held.(*sqlOwnership).conn)).To(Succeed())
		},
		factory: func(options ExpiringOptions) (Locker, error) {
			db, err := sql.Open("sqlserver", testSetting("COMMONS_DB_LEASE_SQLSERVER_URL"))
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(db.Close)
			return NewSQLServer(db, options.Options)
		},
	})
})
