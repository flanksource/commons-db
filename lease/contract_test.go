package lease

import (
	"context"
	"strconv"
	"testing"
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

func TestLease(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Lease Suite")
}

const contractTTL = 3 * time.Second

// backendCapabilities lists the optional lock modes a backend supports, so the
// contract runs every spec a backend can pass and asserts the refusal of the rest.
type backendCapabilities struct{ groups bool }

var allModes = backendCapabilities{groups: true}

func lockerContract(name string, supports backendCapabilities, factory func(ExpiringOptions) (Locker, error)) {
	Describe(name, func() {
		var first, second Locker
		BeforeEach(func() {
			options := ExpiringOptions{Options: Options{Scope: uuid.NewString(), RetryInterval: 10 * time.Millisecond}, TTL: contractTTL}
			var err error
			first, err = factory(options)
			Expect(err).NotTo(HaveOccurred())
			second, err = factory(options)
			Expect(err).NotTo(HaveOccurred())
		})

		It("allows independent keys and excludes writers on a held key", func(ctx SpecContext) {
			lock := first.Mutex("git-azure-oma", as("first"))
			hold(ctx, lock)
			expectBusy(second.ReadWriteLock("git-azure-oma", as("second")).TryLock(ctx))
			expectBusy(second.ReadWriteLock("git-azure-oma", as("second")).TryRLock(ctx))
			hold(ctx, second.Mutex("git-azure-other", as("second")))
		}, SpecTimeout(8*time.Second))

		It("continues excluding contenders past the initial lease duration", func(ctx SpecContext) {
			const key = "renew"
			held := hold(ctx, first.Mutex(key, as("first")))
			Consistently(func(g Gomega) {
				g.Expect(held.Done()).NotTo(BeClosed())
				contender, ok, err := second.Mutex(key, as("second")).TryLock(ctx)
				if ok {
					g.Expect(contender.Unlock(ctx)).To(Succeed())
				}
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(ok).To(BeFalse())
			}, contractTTL+200*time.Millisecond, 100*time.Millisecond).Should(Succeed())
		}, SpecTimeout(8*time.Second))

		It("allows concurrent readers and admits a queued writer before new readers", func(ctx SpecContext) {
			const key = "schema"
			rw := first.ReadWriteLock(key, as("first"))
			read, err := rw.RLock(ctx)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(read.Unlock, ctx)
			other, ok, err := second.ReadWriteLock(key, as("second")).TryRLock(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			DeferCleanup(other.Unlock, ctx)
			waiting := second.Mutex(key, as("second")).LockWait(ctx)
			expectWriterRegistered(ctx, rw)
			if supports.groups {
				expectBusy(first.GroupLock(key, "USA", as("first")).TryLock(ctx))
			}
			Expect(read.Unlock(ctx)).To(Succeed())
			Consistently(waiting, 30*time.Millisecond).ShouldNot(Receive())
			Expect(other.Unlock(ctx)).To(Succeed())
			var result Result
			Eventually(waiting, 2*time.Second).Should(Receive(&result))
			Expect(result.Err).NotTo(HaveOccurred())
			DeferCleanup(result.Lease.Unlock, ctx)
			expectBusy(rw.TryRLock(ctx))
		}, SpecTimeout(8*time.Second))

		It("removes canceled writer intent and releases an acquisition abandoned before channel delivery", func(ctx SpecContext) {
			const key = "cancel"
			rw := first.ReadWriteLock(key, as("first"))
			read, err := rw.RLock(ctx)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(read.Unlock, ctx)
			waitCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			waiting := second.Mutex(key, as("second")).LockWait(waitCtx)
			expectWriterRegistered(ctx, rw)
			cancel()
			Eventually(waiting, 2*time.Second).Should(BeClosed())
			another, err := rw.RLock(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(another.Unlock(ctx)).To(Succeed())
			Expect(read.Unlock(ctx)).To(Succeed())

			waitCtx, cancel = context.WithCancel(ctx)
			defer cancel()
			waiting = second.Mutex(key, as("second")).LockWait(waitCtx)
			expectWriterRegistered(ctx, rw)
			cancel()
			Eventually(waiting, 2*time.Second).Should(BeClosed())
			hold(ctx, first.Mutex(key, as("first")))
		}, SpecTimeout(8*time.Second))

		if !supports.groups {
			It("refuses group locks without touching the key", func(ctx SpecContext) {
				held, acquired, err := first.GroupLock("country", "USA", as("first")).TryLock(ctx)
				Expect(err).To(MatchError(ContainSubstring("group locks are not supported")))
				Expect(held).To(BeNil())
				Expect(acquired).To(BeFalse())
				_, err = second.GroupLock("country", "USA", as("second")).Lock(ctx)
				Expect(err).To(MatchError(ContainSubstring("group locks are not supported")))
				hold(ctx, second.Mutex("country", as("second")))
			}, SpecTimeout(8*time.Second))
			return
		}

		It("atomically admits either a writer or a group across independent clients", func(ctx SpecContext) {
			for range 10 {
				key := uuid.NewString()
				start := make(chan struct{})
				results := make(chan Result, 2)
				for _, lock := range []Lock{first.Mutex(key, as("first")), second.GroupLock(key, "USA", as("second"))} {
					go func() {
						<-start
						held, _, err := lock.TryLock(ctx)
						results <- Result{Lease: held, Err: err}
					}()
				}
				close(start)
				var holders []Lease
				for range 2 {
					var result Result
					Eventually(results, 2*time.Second).Should(Receive(&result))
					Expect(result.Err).NotTo(HaveOccurred())
					if result.Lease != nil {
						holders = append(holders, result.Lease)
					}
				}
				for _, held := range holders {
					Expect(held.Unlock(ctx)).To(Succeed())
				}
				Expect(holders).To(HaveLen(1))
			}
		}, SpecTimeout(15*time.Second))

		It("admits the same group and rejects a different group until its last holder releases", func(ctx SpecContext) {
			const key = "country"
			usa := hold(ctx, first.GroupLock(key, "USA", as("first")))
			otherUSA := hold(ctx, second.GroupLock(key, "USA", as("second")))
			rsa := second.GroupLock(key, "RSA", as("second"))
			expectBusy(rsa.TryLock(ctx))
			expectBusy(second.Mutex(key, as("second")).TryLock(ctx))
			read, err := second.ReadWriteLock(key, as("second")).RLock(ctx)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(read.Unlock, ctx)
			Expect(usa.Unlock(ctx)).To(Succeed())
			expectBusy(rsa.TryLock(ctx))
			Expect(otherUSA.Unlock(ctx)).To(Succeed())
			hold(ctx, rsa)
		}, SpecTimeout(8*time.Second))
	})
}

func hold(ctx context.Context, lock Lock) Lease {
	l, err := lock.Lock(ctx)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(l.Unlock, ctx)
	return l
}

func expectBusy(l Lease, acquired bool, err error) {
	Expect(err).NotTo(HaveOccurred())
	Expect(l).To(BeNil())
	Expect(acquired).To(BeFalse())
}

func expectWriterRegistered(ctx context.Context, rw ReadWriteLock) {
	Eventually(func(g Gomega) bool {
		read, ok, err := rw.TryRLock(ctx)
		g.Expect(err).NotTo(HaveOccurred())
		if ok {
			g.Expect(read.Unlock(ctx)).To(Succeed())
		}
		return !ok
	}, 2*time.Second).Should(BeTrue())
}

var _ = Describe("lease backends", func() {
	client := fake.NewClientset()
	client.PrependReactor("update", "leases", func(action clienttest.Action) (bool, runtime.Object, error) {
		update := action.(clienttest.UpdateAction)
		next := update.GetObject().(*api.Lease).DeepCopy()
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
	lockerContract("Kubernetes", allModes, func(options ExpiringOptions) (Locker, error) {
		return NewKubernetes(client.CoordinationV1().Leases("lease-test"), options)
	})
	files := map[string]string{}
	lockerContract("File", backendCapabilities{}, func(options ExpiringOptions) (Locker, error) {
		if files[options.Scope] == "" {
			files[options.Scope] = GinkgoT().TempDir()
		}
		return NewFile(files[options.Scope], options.Options)
	})
})
