package git_test

import (
	"context"
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"os"
	"path/filepath"
)

type historyFixtureProvider struct {
	recordingProvider
	base    string
	probes  int
	failure error
}

func (p *historyFixtureProvider) RemoteMergeBase(context.Context, git.RemoteMergeBaseOptions) (string, error) {
	p.probes++
	return p.base, p.failure
}

var _ = Describe("shared repository cache", func() {
	It("shares a donor store and creates an independent clone", func(ctx SpecContext) {
		donor := newBareRepo("donor")
		fork := newFork(donor)
		tip := fork.commitOn(fixtureCommit{Branch: "main", Path: "fork.txt", Content: "fork\n", Message: "fork"})
		cache := &git.Cache{Root: fixtureTempDir()}
		donorRepo := mirroredRepository(donor)
		initial, err := cache.Checkout(ctx, donorRepo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(initial.Release()).To(Succeed())
		forkRepo := mirroredRepository(fork)
		history := &historyFixtureProvider{base: donor.Main}
		forkRepo.Provider = history
		tree, err := cache.Checkout(ctx, forkRepo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		released := false
		DeferCleanup(func() {
			if !released {
				Expect(tree.Release()).To(Succeed())
			}
		})
		Expect(tree.Ref.SHA).To(Equal(tip))
		common := gitFixture(tree.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
		Expect(common).To(Equal(filepath.Join(cache.Root, donorRepo.URL.CacheKey(), "mirror.git")))
		Expect(filepath.Join(cache.Root, forkRepo.URL.CacheKey(), "mirror.git")).NotTo(BeAnExistingFile())
		clone := filepath.Join(fixtureTempDir(), "clone")
		_, err = git.Clone(ctx, forkRepo, git.CloneOptions{Dir: clone, Ref: "main", Cache: cache})
		Expect(err).NotTo(HaveOccurred())
		Expect(history.probes).To(Equal(1))
		Expect(gitFixture(clone, "remote", "get-url", "origin")).To(Equal(forkRepo.URL.Clone))
		Expect(gitFixture(clone, "rev-parse", "refs/remotes/origin/main")).To(Equal(tip))
		Expect(tree.Release()).To(Succeed())
		released = true
		Expect(os.RemoveAll(cache.Root)).To(Succeed())
		Expect(gitFixture(clone, "show", "HEAD:fork.txt")).To(Equal("fork"))
		gitFixture(clone, "fsck", "--full")
	})
})
