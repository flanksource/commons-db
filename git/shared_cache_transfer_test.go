package git_test

import (
	"fmt"
	"io/fs"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type packResponseWriter struct {
	http.ResponseWriter
	bytes int64
}

func (w *packResponseWriter) Write(body []byte) (int, error) {
	n, err := w.ResponseWriter.Write(body)
	w.bytes += int64(n)
	return n, err
}
func (w *packResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (h *httpRemote) PackBytes() int64 { h.mu.Lock(); defer h.mu.Unlock(); return h.packBytes }

func transferDiskBytes(path string) int64 {
	GinkgoHelper()
	var bytes int64
	Expect(filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		info, err := entry.Info()
		if err == nil {
			bytes += info.Size()
		}
		return err
	})).To(Succeed())
	return bytes
}

var _ = Describe("scoped object pool transfer", func() {
	It("transfers less pack data when a fork reuses cached objects", func(ctx SpecContext) {
		donor := newBareRepo("large-donor")
		payload := make([]byte, 512*1024)
		_, err := rand.New(rand.NewSource(7)).Read(payload)
		Expect(err).NotTo(HaveOccurred())
		donor.Main = donor.commitOn(fixtureCommit{Branch: "main", Path: "payload.bin", Content: string(payload), Message: "payload"})
		fork := newFork(donor)
		fork.Main = fork.commitOn(fixtureCommit{Branch: "main", Path: "delta.txt", Content: "delta\n", Message: "delta"})
		Expect(os.Symlink(donor.Path, filepath.Join(fork.Root, filepath.Base(donor.Path)))).To(Succeed())
		remote := newHTTPRemote(fork.Root, false)
		shared := &git.Cache{Root: fixtureTempDir()}
		initial, err := shared.Checkout(ctx, organizationRepository(remote, donor, "example"), git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(initial.Release()).To(Succeed())
		repo := organizationRepository(remote, fork, "example")
		ancestry := &unavailableAncestry{}
		repo.Provider = ancestry
		cold := &git.Cache{Root: fixtureTempDir()}
		coldBytes := measureCheckouts(ctx, remote, cold, "cold", "main", []*git.Repository{repo})
		pooledBytes := measureCheckouts(ctx, remote, shared, "pooled", "main", []*git.Repository{repo})
		warmBytes := measureCheckouts(ctx, remote, shared, "warm SHA", fork.Main, []*git.Repository{repo})
		Expect(coldBytes).To(BeNumerically(">", len(payload)))
		Expect(pooledBytes).To(BeNumerically("<", coldBytes/4))
		Expect(warmBytes).To(BeZero())
		Expect(ancestry.calls.Load()).To(BeZero())
	})

	It("keeps concurrent related and independent checkouts on their requested commits", func(ctx SpecContext) {
		donor, fork, remote := organizationFixtures()
		independent := newBareRepo("independent")
		independentPath := filepath.Join(donor.Root, filepath.Base(independent.Path))
		Expect(os.Symlink(independent.Path, independentPath)).To(Succeed())
		independentTip := independent.orphanOn("independent", "unique.txt", "independent\n")
		gitFixture(independent.Path, "update-ref", "refs/heads/main", independentTip)
		cache := &git.Cache{Root: fixtureTempDir()}
		measureCheckouts(ctx, remote, cache, "donor prefetch", donor.Main, []*git.Repository{organizationRepository(remote, donor, "example")})
		related := []*git.Repository{organizationRepository(remote, donor, "example"), organizationRepository(remote, fork, "example")}
		Expect(measureCheckouts(ctx, remote, cache, "concurrent related", donor.Main, related)).To(BeZero())
		unrelated := []*git.Repository{organizationRepository(remote, donor, "another"), organizationRepository(remote, independent, "another")}
		shareNoHistory := organizationRepository(remote, independent, "example")
		Expect(measureCheckouts(ctx, remote, cache, "independent history in the seeded pool", independentTip, []*git.Repository{shareNoHistory})).To(BeNumerically(">", 0))
		Expect(measureCheckouts(ctx, remote, &git.Cache{Root: fixtureTempDir()}, "concurrent unrelated", "main", unrelated)).To(BeNumerically(">", 0))
	})
})

func measureCheckouts(ctx SpecContext, remote *httpRemote, cache *git.Cache, label, ref string, repos []*git.Repository) int64 {
	GinkgoHelper()
	type result struct {
		tree *git.Worktree
		err  error
	}
	beforePack, beforeDisk := remote.PackBytes(), transferDiskBytes(cache.Root)
	results := make(chan result, len(repos))
	start := time.Now()
	for _, repo := range repos {
		go func() {
			tree, err := cache.Checkout(ctx, repo, git.CheckoutOptions{Ref: ref, Ephemeral: true})
			results <- result{tree, err}
		}()
	}
	for range repos {
		var checkout result
		Eventually(ctx, results).WithTimeout(30 * time.Second).Should(Receive(&checkout))
		Expect(checkout.err).NotTo(HaveOccurred())
		defer func() { Expect(checkout.tree.Release()).To(Succeed()) }()
		if len(ref) == 40 {
			Expect(checkout.tree.Ref.SHA).To(Equal(ref))
		}
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	pack, disk := remote.PackBytes()-beforePack, transferDiskBytes(cache.Root)
	AddReportEntry(label, fmt.Sprintf("%d upload-pack bytes, %d local bytes added with leased worktrees, %d total local bytes, %s elapsed", pack, disk-beforeDisk, disk, elapsed))
	return pack
}
