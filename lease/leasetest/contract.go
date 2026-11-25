// Package leasetest holds the ginkgo contract every lease.Locker backend must pass.
package leasetest

import (
	"context"
	"os"
	"time"

	"github.com/flanksource/commons-db/lease"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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
	Describe(name, func() {
		var first, second lease.Locker
		BeforeEach(func() {
			options := lease.ExpiringOptions{Options: lease.Options{Scope: uuid.NewString(), RetryInterval: 10 * time.Millisecond}, TTL: ContractTTL}
			var err error
			first, err = factory(options)
			Expect(err).NotTo(HaveOccurred())
			second, err = factory(options)
			Expect(err).NotTo(HaveOccurred())
		})

		It("allows independent keys and excludes writers on a held key", func(ctx SpecContext) {
			Hold(ctx, first.Mutex("git-azure-oma", As("first")))
			ExpectBusy(second.ReadWriteLock("git-azure-oma", As("second")).TryLock(ctx))
			ExpectBusy(second.ReadWriteLock("git-azure-oma", As("second")).TryRLock(ctx))
			Hold(ctx, second.Mutex("git-azure-other", As("second")))
		}, SpecTimeout(8*time.Second))

		It("continues excluding contenders past the initial lease duration", func(ctx SpecContext) {
			const key = "renew"
			held := Hold(ctx, first.Mutex(key, As("first")))
			Consistently(func(g Gomega) {
				g.Expect(held.Done()).NotTo(BeClosed())
				contender, ok, err := second.Mutex(key, As("second")).TryLock(ctx)
				if ok {
					g.Expect(contender.Unlock(ctx)).To(Succeed())
				}
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(ok).To(BeFalse())
			}, ContractTTL+200*time.Millisecond, 100*time.Millisecond).Should(Succeed())
		}, SpecTimeout(8*time.Second))

		It("allows concurrent readers and admits a queued writer before new readers", func(ctx SpecContext) {
			const key = "schema"
			rw := first.ReadWriteLock(key, As("first"))
			read, err := rw.RLock(ctx)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(read.Unlock, ctx)
			other, ok, err := second.ReadWriteLock(key, As("second")).TryRLock(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeTrue())
			DeferCleanup(other.Unlock, ctx)
			waiting := second.Mutex(key, As("second")).LockWait(ctx)
			expectWriterRegistered(ctx, rw)
			if supports.Groups {
				ExpectBusy(first.GroupLock(key, "USA", As("first")).TryLock(ctx))
			}
			Expect(read.Unlock(ctx)).To(Succeed())
			Consistently(waiting, 30*time.Millisecond).ShouldNot(Receive())
			Expect(other.Unlock(ctx)).To(Succeed())
			var result lease.Result
			Eventually(waiting, 2*time.Second).Should(Receive(&result))
			Expect(result.Err).NotTo(HaveOccurred())
			DeferCleanup(result.Lease.Unlock, ctx)
			ExpectBusy(rw.TryRLock(ctx))
		}, SpecTimeout(8*time.Second))

		It("removes canceled writer intent and releases an acquisition abandoned before channel delivery", func(ctx SpecContext) {
			const key = "cancel"
			rw := first.ReadWriteLock(key, As("first"))
			read, err := rw.RLock(ctx)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(read.Unlock, ctx)
			waitCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			waiting := second.Mutex(key, As("second")).LockWait(waitCtx)
			expectWriterRegistered(ctx, rw)
			cancel()
			Eventually(waiting, 2*time.Second).Should(BeClosed())
			another, err := rw.RLock(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(another.Unlock(ctx)).To(Succeed())
			Expect(read.Unlock(ctx)).To(Succeed())

			waitCtx, cancel = context.WithCancel(ctx)
			defer cancel()
			acquired := make(chan struct{})
			writer := *second.(*lease.Backend)
			begin := writer.Begin
			writer.Begin = func(req lease.Request, wait bool) lease.Acquisition {
				return signaledAcquisition{Acquisition: begin(req, wait), acquired: acquired}
			}
			waiting = writer.Mutex(key, As("second")).LockWait(waitCtx)
			Eventually(acquired, 2*time.Second).Should(BeClosed())
			cancel()
			Eventually(waiting, 2*time.Second).Should(BeClosed())
			Hold(ctx, first.Mutex(key, As("first")))
		}, SpecTimeout(8*time.Second))

		if !supports.Groups {
			It("refuses group locks without touching the key", func(ctx SpecContext) {
				held, acquired, err := first.GroupLock("country", "USA", As("first")).TryLock(ctx)
				Expect(err).To(MatchError(ContainSubstring("group locks are not supported")))
				Expect(held).To(BeNil())
				Expect(acquired).To(BeFalse())
				_, err = second.GroupLock("country", "USA", As("second")).Lock(ctx)
				Expect(err).To(MatchError(ContainSubstring("group locks are not supported")))
				Hold(ctx, second.Mutex("country", As("second")))
			}, SpecTimeout(8*time.Second))
			return
		}

		It("atomically admits either a writer or a group across independent clients", func(ctx SpecContext) {
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
			usa := Hold(ctx, first.GroupLock(key, "USA", As("first")))
			otherUSA := Hold(ctx, second.GroupLock(key, "USA", As("second")))
			rsa := second.GroupLock(key, "RSA", As("second"))
			ExpectBusy(rsa.TryLock(ctx))
			ExpectBusy(second.Mutex(key, As("second")).TryLock(ctx))
			read, err := second.ReadWriteLock(key, As("second")).RLock(ctx)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(read.Unlock, ctx)
			Expect(usa.Unlock(ctx)).To(Succeed())
			ExpectBusy(rsa.TryLock(ctx))
			Expect(otherUSA.Unlock(ctx)).To(Succeed())
			Hold(ctx, rsa)
		}, SpecTimeout(8*time.Second))
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
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(l.Unlock, ctx)
	return l
}

// ExpectBusy asserts a TryLock result reports contention without an error.
func ExpectBusy(l lease.Lease, acquired bool, err error) {
	Expect(err).NotTo(HaveOccurred())
	Expect(l).To(BeNil())
	Expect(acquired).To(BeFalse())
}

func expectWriterRegistered(ctx context.Context, rw lease.ReadWriteLock) {
	Eventually(func(g Gomega) bool {
		read, ok, err := rw.TryRLock(ctx)
		g.Expect(err).NotTo(HaveOccurred())
		if ok {
			g.Expect(read.Unlock(ctx)).To(Succeed())
		}
		return !ok
	}, 2*time.Second).Should(BeTrue())
}

// TestSetting returns the value of the named environment variable, or skips the spec when it is unset.
func TestSetting(name string) string {
	value := os.Getenv(name)
	if value == "" {
		Skip("set " + name + " to a disposable test service")
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
	Describe(name+" holders", func() {
		var first, second lease.Locker
		BeforeEach(func() {
			options := lease.ExpiringOptions{Options: lease.Options{Scope: uuid.NewString(), RetryInterval: 10 * time.Millisecond}, TTL: ContractTTL}
			var err error
			first, err = backend.Factory(options)
			Expect(err).NotTo(HaveOccurred())
			second, err = backend.Factory(options)
			Expect(err).NotTo(HaveOccurred())
		})

		It("reports the writer whose lease made a TryLock fail", func(ctx SpecContext) {
			before := time.Now().Add(-time.Second)
			Hold(ctx, first.Mutex("deploy", As("alpha")))
			contender := second.Mutex("deploy", As("beta"))
			ExpectBusy(contender.TryLock(ctx))
			Expect(holdersOf(ctx, contender, before)).To(Equal([]lease.Holder{{Name: "alpha", Mode: "write"}}))
		}, SpecTimeout(8*time.Second))

		It("lists every reader and every group holder by name", func(ctx SpecContext) {
			before := time.Now().Add(-time.Second)
			for _, reader := range []lease.ReadWriteLock{first.ReadWriteLock("schema", As("reader-1")), second.ReadWriteLock("schema", As("reader-2"))} {
				held, err := reader.RLock(ctx)
				Expect(err).NotTo(HaveOccurred())
				DeferCleanup(held.Unlock, ctx)
			}
			Expect(holdersOf(ctx, first.Mutex("schema", As("probe")), before)).To(ConsistOf(
				lease.Holder{Name: "reader-1", Mode: "read"}, lease.Holder{Name: "reader-2", Mode: "read"}))

			Hold(ctx, first.GroupLock("country", "USA", As("usa-1")))
			Hold(ctx, second.GroupLock("country", "USA", As("usa-2")))
			group := "USA"
			if !backend.NamesGroups {
				group = ""
			}
			Expect(holdersOf(ctx, second.GroupLock("country", "RSA", As("rsa")), before)).To(ConsistOf(
				lease.Holder{Name: "usa-1", Mode: "group", Group: group}, lease.Holder{Name: "usa-2", Mode: "group", Group: group}))
		}, SpecTimeout(8*time.Second))

		It("reports no holder after unlock", func(ctx SpecContext) {
			held := Hold(ctx, first.Mutex("deploy", As("alpha")))
			Expect(held.Unlock(ctx)).To(Succeed())
			Expect(second.Mutex("deploy", As("beta")).Holders(ctx)).To(BeEmpty())
		}, SpecTimeout(8*time.Second))

		It("drops a holder whose ownership is lost", func(ctx SpecContext) {
			probe := second.Mutex("deploy", As("beta"))
			firstBackend := first.(*lease.Backend)
			req := lease.Request{Scope: firstBackend.Options.Scope, Key: "deploy", Holder: "stale", Mode: lease.WriteMode}
			stale, err := firstBackend.Begin(req, false).Try(ctx)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func(ctx SpecContext) error { return stale.Unlock(ctx) })
			Expect(probe.Holders(ctx)).To(ConsistOf(HaveField("Name", "stale")))
			backend.Lose(ctx, stale)
			Eventually(func(g Gomega) {
				holders, err := probe.Holders(ctx)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(holders).To(BeEmpty())
			}, 3*ContractTTL, 100*time.Millisecond).Should(Succeed())
		}, SpecTimeout(15*time.Second))
	})
}

// holdersOf lists the holders of lock, checks each Since lies between
// before and now, and clears it so the caller can compare the rest.
func holdersOf(ctx context.Context, lock lease.Lock, before time.Time) []lease.Holder {
	holders, err := lock.Holders(ctx)
	Expect(err).NotTo(HaveOccurred())
	for i := range holders {
		Expect(holders[i].Since).To(BeTemporally(">=", before))
		Expect(holders[i].Since).To(BeTemporally("<=", time.Now().Add(time.Second)))
		holders[i].Since = time.Time{}
	}
	return holders
}

// ExpireUnrenewed relies on the backend's TTL to retire an ownership that is
// never renewed.
func ExpireUnrenewed(context.Context, lease.Ownership) {}
