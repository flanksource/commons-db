package git_test

import (
	"context"
	"encoding/json"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"os"
	"path/filepath"
)

// historyFixtureProvider answers RemoteMergeBase with bases[BaseSHA] when
// bases is set (an unknown SHA is not on the remote), else with base/failure.
type historyFixtureProvider struct {
	recordingProvider
	base    string
	bases   map[string]string
	probes  int
	failure error
}

func (p *historyFixtureProvider) RemoteMergeBase(_ context.Context, options git.RemoteMergeBaseOptions) (string, error) {
	p.probes++
	if p.bases == nil {
		return p.base, p.failure
	}
	if base, ok := p.bases[options.BaseSHA]; ok {
		return base, nil
	}
	return "", git.ErrRevisionNotFound
}

// cacheMirror caches main of fixture in its own mirror (no shared history
// provider), so it can become the owner other repositories bind to.
func cacheMirror(ctx context.Context, cache *git.Cache, fixture bareRepo) *git.Repository {
	GinkgoHelper()
	repo := mirroredRepository(fixture)
	tree, err := cache.Checkout(ctx, repo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
	Expect(err).NotTo(HaveOccurred())
	Expect(tree.Release()).To(Succeed())
	return repo
}

func cacheOwner(cache *git.Cache, repo *git.Repository) string {
	GinkgoHelper()
	data, err := os.ReadFile(filepath.Join(cache.Root, repo.URL.CacheKey(), "repository.json"))
	Expect(err).NotTo(HaveOccurred())
	var binding struct{ Owner string }
	Expect(json.Unmarshal(data, &binding)).To(Succeed())
	return binding.Owner
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

	It("binds to the mirror sharing the most history, not the most recently cached one", func(ctx SpecContext) {
		upstream := newBareRepo("upstream")
		far := newFork(upstream)
		farTip := far.commitOn(fixtureCommit{Branch: "main", Path: "far.txt", Content: "far\n", Message: "far"})
		upstream.commitOn(fixtureCommit{Branch: "main", Path: "a.txt", Content: "a\n", Message: "upstream a"})
		recent := upstream.commitOn(fixtureCommit{Branch: "main", Path: "b.txt", Content: "b\n", Message: "upstream b"})
		near := newFork(upstream)
		nearTip := near.commitOn(fixtureCommit{Branch: "main", Path: "near.txt", Content: "near\n", Message: "near"})
		target := newFork(upstream)
		targetTip := target.commitOn(fixtureCommit{Branch: "main", Path: "target.txt", Content: "target\n", Message: "target"})

		cache := &git.Cache{Root: fixtureTempDir()}
		nearRepo := cacheMirror(ctx, cache, near)
		cacheMirror(ctx, cache, far)
		targetRepo := mirroredRepository(target)
		history := &historyFixtureProvider{bases: map[string]string{farTip: upstream.Main, nearTip: recent}}
		targetRepo.Provider = history

		var mirrors []string
		observed := git.WithFetchObserver(ctx, func(_ context.Context, progress git.FetchProgress) {
			mirrors = append(mirrors, progress.Mirror)
		})
		tree, err := cache.Checkout(observed, targetRepo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(tree.Release)

		Expect(mirrors).NotTo(BeEmpty())
		Expect(mirrors).To(HaveEach(nearRepo.URL.CacheKey()))
		Expect(tree.Ref.SHA).To(Equal(targetTip))
		Expect(gitFixture(tree.Dir, "rev-parse", "--path-format=absolute", "--git-common-dir")).
			To(Equal(filepath.Join(cache.Root, nearRepo.URL.CacheKey(), "mirror.git")))
		Expect(cacheOwner(cache, targetRepo)).To(Equal(nearRepo.URL.CacheKey()))
		Expect(history.probes).To(Equal(2))
	})
})
