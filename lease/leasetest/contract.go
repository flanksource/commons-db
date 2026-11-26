// Package leasetest holds the ginkgo contract every lease.Locker backend must pass.
package leasetest

import (
	"context"
	"os"
	"time"

	"github.com/flanksource/commons-db/lease"
	"github.com/google/uuid"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// ContractTTL is the lease TTL every contract spec runs with.
const ContractTTL = 3 * time.Second

// Capabilities lists the optional lock modes a backend supports, so the
// contract runs every spec a backend can pass and asserts the refusal of the rest.
type Capabilities struct{ Groups bool }

// AllModes is the capability set of a backend that supports every lock mode.
var AllModes = Capabilities{Groups: true}

// Factory builds a locker; the contract calls it twice per spec with the same
// options to model two independent clients.
type Factory func(lease.ExpiringOptions) (lease.Locker, error)

// LockerContract registers the exclusion, queueing and group specs for a backend.
func LockerContract(name string, supports Capabilities, factory Factory) {
	ginkgo.Describe(name, func() {
		var first, second lease.Locker
		ginkgo.BeforeEach(func() {
			options := lease.ExpiringOptions{Options: lease.Options{Scope: uuid.NewString(), RetryInterval: 10 * time.Millisecond}, TTL: ContractTTL}
			var err error
			first, err = factory(options)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			second, err = factory(options)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("allows independent keys and excludes writers on a held key", func(ctx ginkgo.SpecContext) {
			Hold(ctx, first.Mutex("git-azure-oma", As("first")))
			ExpectBusy(second.ReadWriteLock("git-azure-oma", As("second")).TryLock(ctx))
			ExpectBusy(second.ReadWriteLock("git-azure-oma", As("second")).TryRLock(ctx))
			Hold(ctx, second.Mutex("git-azure-other", As("second")))
		}, ginkgo.SpecTimeout(8*time.Second))

		ginkgo.It("continues excluding contenders past the initial lease duration", func(ctx ginkgo.SpecContext) {
			const key = "renew"
			held := Hold(ctx, first.Mutex(key, As("first")))
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(held.Done()).NotTo(gomega.BeClosed())
				contender, ok, err := second.Mutex(key, As("second")).TryLock(ctx)
				if ok {
					g.Expect(contender.Unlock(ctx)).To(gomega.Succeed())
				}
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(ok).To(gomega.BeFalse())
			}, ContractTTL+200*time.Millisecond, 100*time.Millisecond).Should(gomega.Succeed())
		}, ginkgo.SpecTimeout(8*time.Second))

		ginkgo.It("allows concurrent readers and admits a queued writer before new readers", func(ctx ginkgo.SpecContext) {
			const key = "schema"
			rw := first.ReadWriteLock(key, As("first"))
			read, err := rw.RLock(ctx)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			ginkgo.DeferCleanup(read.Unlock, ctx)
			other, ok, err := second.ReadWriteLock(key, As("second")).TryRLock(ctx)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(ok).To(gomega.BeTrue())
			ginkgo.DeferCleanup(other.Unlock, ctx)
			waiting := second.Mutex(key, As("second")).LockWait(ctx)
			expectWriterRegistered(ctx, rw)
			if supports.Groups {
				ExpectBusy(first.GroupLock(key, "USA", As("first")).TryLock(ctx))
			}
			gomega.Expect(read.Unlock(ctx)).To(gomega.Succeed())
			gomega.Consistently(waiting, 30*time.Millisecond).ShouldNot(gomega.Receive())
			gomega.Expect(other.Unlock(ctx)).To(gomega.Succeed())
			var result lease.Result
			gomega.Eventually(waiting, 2*time.Second).Should(gomega.Receive(&result))
			gomega.Expect(result.Err).NotTo(gomega.HaveOccurred())
			ginkgo.DeferCleanup(result.Lease.Unlock, ctx)
			ExpectBusy(rw.TryRLock(ctx))
		}, ginkgo.SpecTimeout(8*time.Second))

		ginkgo.It("removes canceled writer intent and releases an acquisition abandoned before channel delivery", func(ctx ginkgo.SpecContext) {
			const key = "cancel"
			rw := first.ReadWriteLock(key, As("first"))
			read, err := rw.RLock(ctx)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			ginkgo.DeferCleanup(read.Unlock, ctx)
			waitCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			waiting := second.Mutex(key, As("second")).LockWait(waitCtx)
			expectWriterRegistered(ctx, rw)
			cancel()
			gomega.Eventually(waiting, 2*time.Second).Should(gomega.BeClosed())
			another, err := rw.RLock(ctx)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(another.Unlock(ctx)).To(gomega.Succeed())
			gomega.Expect(read.Unlock(ctx)).To(gomega.Succeed())

			waitCtx, cancel = context.WithCancel(ctx)
			defer cancel()
			acquired := make(chan struct{})
			writer := *second.(*lease.Backend)
			begin := writer.Begin
			writer.Begin = func(req lease.Request, wait bool) lease.Acquisition {
				return signaledAcquisition{Acquisition: begin(req, wait), acquired: acquired}
			}
			waiting = writer.Mutex(key, As("second")).LockWait(waitCtx)
			gomega.Eventually(acquired, 2*time.Second).Should(gomega.BeClosed())
			cancel()
			gomega.Eventually(waiting, 2*time.Second).Should(gomega.BeClosed())
			Hold(ctx, first.Mutex(key, As("first")))
		}, ginkgo.SpecTimeout(8*time.Second))

		if !supports.Groups {
			ginkgo.It("refuses group locks without touching the key", func(ctx ginkgo.SpecContext) {
				held, acquired, err := first.GroupLock("country", "USA", As("first")).TryLock(ctx)
				gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("group locks are not supported")))
				gomega.Expect(held).To(gomega.BeNil())
				gomega.Expect(acquired).To(gomega.BeFalse())
				_, err = second.GroupLock("country", "USA", As("second")).Lock(ctx)
				gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("group locks are not supported")))
				Hold(ctx, second.Mutex("country", As("second")))
			}, ginkgo.SpecTimeout(8*time.Second))
			return
		}

		ginkgo.It("atomically admits either a writer or a group across independent clients", func(ctx ginkgo.SpecContext) {
			for range 10 {
				key := uuid.NewString()
				start := make(chan struct{})
				results := make(chan lease.Result, 2)
				for _, lock := range []lease.Lock{first.Mutex(key, As("first")), second.GroupLock(key, "USA", As("second"))} {
					go func() {
						<-start
						held, _, err := lock.TryLock(ctx)
						results <- lease.Result{Lease: held, Err: err}
					}()
				}
				close(start)
				var holders []lease.Lease
				for range 2 {
					var result lease.Result
					gomega.Eventually(results, 2*time.Second).Should(gomega.Receive(&result))
					gomega.Expect(result.Err).NotTo(gomega.HaveOccurred())
					if result.Lease != nil {
						holders = append(holders, result.Lease)
					}
				}
				for _, held := range holders {
					gomega.Expect(held.Unlock(ctx)).To(gomega.Succeed())
				}
				gomega.Expect(holders).To(gomega.HaveLen(1))
			}
		}, ginkgo.SpecTimeout(15*time.Second))

		ginkgo.It("admits the same group and rejects a different group until its last holder releases", func(ctx ginkgo.SpecContext) {
			const key = "country"
			usa := Hold(ctx, first.GroupLock(key, "USA", As("first")))
			otherUSA := Hold(ctx, second.GroupLock(key, "USA", As("second")))
			rsa := second.GroupLock(key, "RSA", As("second"))
			ExpectBusy(rsa.TryLock(ctx))
			ExpectBusy(second.Mutex(key, As("second")).TryLock(ctx))
			read, err := second.ReadWriteLock(key, As("second")).RLock(ctx)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			ginkgo.DeferCleanup(read.Unlock, ctx)
			gomega.Expect(usa.Unlock(ctx)).To(gomega.Succeed())
			ExpectBusy(rsa.TryLock(ctx))
			gomega.Expect(otherUSA.Unlock(ctx)).To(gomega.Succeed())
			Hold(ctx, rsa)
		}, ginkgo.SpecTimeout(8*time.Second))
	})
}

type signaledAcquisition struct {
	lease.Acquisition
	acquired chan struct{}
}

func (a signaledAcquisition) Try(ctx context.Context) (lease.Ownership, error) {
	held, err := a.Acquisition.Try(ctx)
	if held != nil && err == nil {
		close(a.acquired)
	}
	return held, err
}

// As names the caller of an acquisition.
func As(name string) lease.LockOptions { return lease.LockOptions{Holder: name} }

// Hold acquires lock and releases it when the spec ends.
func Hold(ctx context.Context, lock lease.Lock) lease.Lease {
	l, err := lock.Lock(ctx)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	ginkgo.DeferCleanup(l.Unlock, ctx)
	return l
}

// ExpectBusy asserts a TryLock result reports contention without an error.
func ExpectBusy(l lease.Lease, acquired bool, err error) {
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(l).To(gomega.BeNil())
	gomega.Expect(acquired).To(gomega.BeFalse())
}

func expectWriterRegistered(ctx context.Context, rw lease.ReadWriteLock) {
	gomega.Eventually(func(g gomega.Gomega) bool {
		read, ok, err := rw.TryRLock(ctx)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		if ok {
			g.Expect(read.Unlock(ctx)).To(gomega.Succeed())
		}
		return !ok
	}, 2*time.Second).Should(gomega.BeTrue())
}

// TestSetting returns the value of the named environment variable, or skips the spec when it is unset.
func TestSetting(name string) string {
	value := os.Getenv(name)
	if value == "" {
		ginkgo.Skip("set " + name + " to a disposable test service")
	}
	return value
}

// HolderBackend describes how a backend reports holders: Lose ends an
// ownership without unlocking it, and NamesGroups says whether a group
// holder carries its group name.
type HolderBackend struct {
	Factory     Factory
	Lose        func(context.Context, lease.Ownership)
	NamesGroups bool
}

// HoldersContract registers the Holders specs for a backend.
func HoldersContract(name string, backend HolderBackend) {
	ginkgo.Describe(name+" holders", func() {
		var first, second lease.Locker
		ginkgo.BeforeEach(func() {
			options := lease.ExpiringOptions{Options: lease.Options{Scope: uuid.NewString(), RetryInterval: 10 * time.Millisecond}, TTL: ContractTTL}
			var err error
			first, err = backend.Factory(options)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			second, err = backend.Factory(options)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("reports the writer whose lease made a TryLock fail", func(ctx ginkgo.SpecContext) {
			before := time.Now().Add(-time.Second)
			Hold(ctx, first.Mutex("deploy", As("alpha")))
			contender := second.Mutex("deploy", As("beta"))
			ExpectBusy(contender.TryLock(ctx))
			gomega.Expect(holdersOf(ctx, contender, before)).To(gomega.Equal([]lease.Holder{{Name: "alpha", Mode: "write"}}))
		}, ginkgo.SpecTimeout(8*time.Second))

		ginkgo.It("lists every reader and every group holder by name", func(ctx ginkgo.SpecContext) {
			before := time.Now().Add(-time.Second)
			for _, reader := range []lease.ReadWriteLock{first.ReadWriteLock("schema", As("reader-1")), second.ReadWriteLock("schema", As("reader-2"))} {
				held, err := reader.RLock(ctx)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				ginkgo.DeferCleanup(held.Unlock, ctx)
			}
			gomega.Expect(holdersOf(ctx, first.Mutex("schema", As("probe")), before)).To(gomega.ConsistOf(
				lease.Holder{Name: "reader-1", Mode: "read"}, lease.Holder{Name: "reader-2", Mode: "read"}))

			Hold(ctx, first.GroupLock("country", "USA", As("usa-1")))
			Hold(ctx, second.GroupLock("country", "USA", As("usa-2")))
			group := "USA"
			if !backend.NamesGroups {
				group = ""
			}
			gomega.Expect(holdersOf(ctx, second.GroupLock("country", "RSA", As("rsa")), before)).To(gomega.ConsistOf(
				lease.Holder{Name: "usa-1", Mode: "group", Group: group}, lease.Holder{Name: "usa-2", Mode: "group", Group: group}))
		}, ginkgo.SpecTimeout(8*time.Second))

		ginkgo.It("reports no holder after unlock", func(ctx ginkgo.SpecContext) {
			held := Hold(ctx, first.Mutex("deploy", As("alpha")))
			gomega.Expect(held.Unlock(ctx)).To(gomega.Succeed())
			gomega.Expect(second.Mutex("deploy", As("beta")).Holders(ctx)).To(gomega.BeEmpty())
		}, ginkgo.SpecTimeout(8*time.Second))

		ginkgo.It("drops a holder whose ownership is lost", func(ctx ginkgo.SpecContext) {
			probe := second.Mutex("deploy", As("beta"))
			firstBackend := first.(*lease.Backend)
			req := lease.Request{Scope: firstBackend.Options.Scope, Key: "deploy", Holder: "stale", Mode: lease.WriteMode}
			stale, err := firstBackend.Begin(req, false).Try(ctx)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			ginkgo.DeferCleanup(func(ctx ginkgo.SpecContext) error { return stale.Unlock(ctx) })
			gomega.Expect(probe.Holders(ctx)).To(gomega.ConsistOf(gomega.HaveField("Name", "stale")))
			backend.Lose(ctx, stale)
			gomega.Eventually(func(g gomega.Gomega) {
				holders, err := probe.Holders(ctx)
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(holders).To(gomega.BeEmpty())
			}, 3*ContractTTL, 100*time.Millisecond).Should(gomega.Succeed())
		}, ginkgo.SpecTimeout(15*time.Second))
	})
}

// holdersOf lists the holders of lock, checks each Since lies between
// before and now, and clears it so the caller can compare the rest.
func holdersOf(ctx context.Context, lock lease.Lock, before time.Time) []lease.Holder {
	holders, err := lock.Holders(ctx)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	for i := range holders {
		gomega.Expect(holders[i].Since).To(gomega.BeTemporally(">=", before))
		gomega.Expect(holders[i].Since).To(gomega.BeTemporally("<=", time.Now().Add(time.Second)))
		holders[i].Since = time.Time{}
	}
	return holders
}

// ExpireUnrenewed relies on the backend's TTL to retire an ownership that is
// never renewed.
func ExpireUnrenewed(context.Context, lease.Ownership) {}
