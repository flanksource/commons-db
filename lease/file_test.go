package lease

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("File holders", func() {
	It("reports live readers and writers and drops a record its process left behind", func(ctx SpecContext) {
		dir := GinkgoT().TempDir()
		locks, err := NewFile(dir, Options{Scope: "holders"})
		Expect(err).NotTo(HaveOccurred())
		pool := locks.ReadWriteLock("pool", LockOptions{Holder: "pod-a"})
		for _, reader := range []ReadWriteLock{pool, locks.ReadWriteLock("pool", LockOptions{Holder: "pod-b"})} {
			held, err := reader.RLock(ctx)
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(held.Unlock, ctx)
		}
		records := filepath.Join(dir, digest("holders", "pool")+".holders")
		Expect(os.WriteFile(filepath.Join(records, "exited"), []byte(`{"name":"pod-gone","mode":"read"}`), 0o644)).To(Succeed())

		holders, err := pool.Holders(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(holders).To(ConsistOf(
			And(HaveField("Name", "pod-a"), HaveField("Mode", "read")),
			And(HaveField("Name", "pod-b"), HaveField("Mode", "read")),
		))
		Expect(filepath.Join(records, "exited")).NotTo(BeAnExistingFile())

		writer := locks.Mutex("leader", LockOptions{Holder: "pod-c"})
		held := hold(ctx, writer)
		holders, err = writer.Holders(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(holders).To(ConsistOf(And(HaveField("Name", "pod-c"), HaveField("Mode", "write"))))
		Expect(held.Unlock(ctx)).To(Succeed())
		Expect(writer.Holders(ctx)).To(BeEmpty())
	})
})
