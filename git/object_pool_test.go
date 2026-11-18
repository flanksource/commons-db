package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type unavailableAncestry struct{ recordingProvider }

func (*unavailableAncestry) RemoteMergeBase(context.Context, git.RemoteMergeBaseOptions) (string, error) {
	return "", errors.New("provider ancestry API unavailable")
}

func organizationFixtures() (bareRepo, bareRepo, *httpRemote) {
	GinkgoHelper()
	donor := newBareRepo("donor")
	fork := newFork(donor)
	Expect(os.Symlink(fork.Path, filepath.Join(donor.Root, filepath.Base(fork.Path)))).To(Succeed())
	return donor, fork, newHTTPRemote(donor.Root, false)
}

func organizationRepository(remote *httpRemote, fixture bareRepo, owner string) *git.Repository {
	repo := newRepository(remote.URL(fixture), git.Auth{Username: remote.User, Password: remote.Password})
	repo.URL.Provider, repo.URL.Owner = models.ConnectionTypeGithub, owner
	return repo
}

var _ = Describe("scoped object pool", func() {
	It("uses a sibling's prefetched commit without contacting the requested remote", func(ctx SpecContext) {
		donor, fork, remote := organizationFixtures()
		source := organizationRepository(remote, donor, "Example")
		target := organizationRepository(remote, fork, "example")
		target.Auth = git.Auth{}
		target.Provider = &unavailableAncestry{}
		cache := &git.Cache{Root: fixtureTempDir()}
		Expect(cache.Fetch(ctx, source, git.FetchOptions{Refs: []git.Ref{branchRef("main")}})).To(Succeed())
		remote.Server.Close()
		worktree, err := (&git.Cache{Root: cache.Root}).Checkout(ctx, target, git.CheckoutOptions{Ref: donor.Main, Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			if worktree != nil {
				Expect(worktree.Release()).To(Succeed())
			}
		})
		common := gitFixture(worktree.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
		Expect(filepath.Base(filepath.Dir(common))).To(Equal(target.URL.CacheKey()))
		Expect(filepath.Join(common, "objects", "info", "alternates")).To(BeAnExistingFile())
		clone := filepath.Join(fixtureTempDir(), "clone")
		_, err = git.Clone(ctx, target, git.CloneOptions{Cache: cache, Dir: clone, Ref: donor.Main})
		Expect(err).NotTo(HaveOccurred())
		Expect(worktree.Release()).To(Succeed())
		worktree = nil
		Expect(os.RemoveAll(cache.Root)).To(Succeed())
		gitFixture(clone, "fsck", "--full")
	})

	DescribeTable("publishes a sibling's own durable checkpoint", func(ctx SpecContext, newStore func(string) git.Store) {
		donor, fork, remote := organizationFixtures()
		fork.Main = fork.commitOn(fixtureCommit{Branch: "main", Path: "fork.txt", Content: "fork\n", Message: "fork"})
		source, target := organizationRepository(remote, donor, "example"), organizationRepository(remote, fork, "example")
		store := newStore(fixtureTempDir())
		cache := &git.Cache{Root: fixtureTempDir(), Store: store}
		Expect(cache.Fetch(ctx, source, git.FetchOptions{Refs: []git.Ref{branchRef("main")}})).To(Succeed())
		Expect(cache.Fetch(ctx, target, git.FetchOptions{Refs: []git.Ref{branchRef("main")}, Publish: true})).To(Succeed())
		entries, err := store.Inventory(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveExactElements(HaveField("Key", target.URL.CacheKey())))
		remote.Server.Close()
		fresh := &git.Cache{Root: fixtureTempDir(), Store: store}
		tree, err := fresh.Checkout(ctx, target, git.CheckoutOptions{Ref: fork.Main, Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(tree.Release)
		Expect(readWorktreeFile(tree, "fork.txt")).To(Equal("fork\n"))
	}, Entry("mirror store", func(path string) git.Store { return &git.MirrorStore{Path: path} }),
		Entry("pack store", func(path string) git.Store { return &git.PackStore{Path: path, CompactAbove: 2} }))
})
