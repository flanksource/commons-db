package lease_test

import (
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/lease"
	"github.com/flanksource/commons-db/lease/leasetest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("File holders", func() {
	It("reports live readers and writers and drops a record its process left behind", func(ctx SpecContext) {
		dir := GinkgoT().TempDir()
		locks, err := lease.NewFile(dir, lease.Options{Scope: "holders"})
		Expect(err).NotTo(HaveOccurred())
		pool := locks.ReadWriteLock("pool", lease.LockOptions{Holder: "pod-a"})
		for _, reader := range []lease.ReadWriteLock{pool, locks.ReadWriteLock("pool", lease.LockOptions{Holder: "pod-b"})} {
			held, err := reader.RLock(ctx)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(held.Unlock, ctx)
		}
		records := filepath.Join(dir, lease.Digest("holders", "pool")+".holders")
		Expect(os.WriteFile(filepath.Join(records, "exited"), []byte(`{"name":"pod-gone","mode":"read"}`), 0o644)).To(Succeed())

		holders, err := pool.Holders(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(holders).To(ConsistOf(
			And(HaveField("Name", "pod-a"), HaveField("Mode", "read")),
			And(HaveField("Name", "pod-b"), HaveField("Mode", "read")),
		))
		Expect(filepath.Join(records, "exited")).NotTo(BeAnExistingFile())

		writer := locks.Mutex("leader", lease.LockOptions{Holder: "pod-c"})
		held := leasetest.Hold(ctx, writer)
		holders, err = writer.Holders(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(holders).To(ConsistOf(And(HaveField("Name", "pod-c"), HaveField("Mode", "write"))))
		Expect(held.Unlock(ctx)).To(Succeed())
		Expect(writer.Holders(ctx)).To(BeEmpty())
	})
})
