package git_test

import (
	"context"
	"errors"
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"os"
	"path/filepath"
	"time"
)

var _ = Describe("shared cache lifecycle", func() {
	var cache *git.Cache
	var donor, fork bareRepo
	var donorRepo, forkRepo *git.Repository
	var provider *historyFixtureProvider
	BeforeEach(func(ctx SpecContext) {
		donor = newBareRepo("donor")
		fork = newFork(donor)
		gitFixture(fork.Path, "config", "http.receivepack", "true")
		fork.commitOn(fixtureCommit{Branch: "main", Path: "fork.txt", Content: "fork\n", Message: "fork"})
		cache = &git.Cache{Root: fixtureTempDir()}
		donorRepo = mirroredRepository(donor)
		tree, err := cache.Checkout(ctx, donorRepo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(tree.Release()).To(Succeed())
		forkRepo = mirroredRepository(fork)
		provider = &historyFixtureProvider{base: donor.Main}
		forkRepo.Provider = provider
	})
	It("routes tree reads through a binding after restart and ref advancement", func(ctx SpecContext) {
		_, err := cache.Show(ctx, forkRepo, fork.Main, "README.md")
		Expect(err).NotTo(HaveOccurred())
		tip := fork.commitOn(fixtureCommit{Branch: "main", Path: "later.txt", Content: "later\n", Message: "advance"})
		restarted := &git.Cache{Root: cache.Root}
		data, err := restarted.Show(ctx, forkRepo, tip, "later.txt")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("later\n"))
		Expect(provider.probes).To(Equal(1))
		Expect(gitFixture(filepath.Join(cache.Root, donorRepo.URL.CacheKey(), "mirror.git"), "rev-parse", "refs/remotes/origin/main")).To(Equal(donor.Main))
	})
	It("isolates equal branch and annotated tag names and pushes to the correct remote", func(ctx SpecContext) {
		donorRepo.Provider = &historyFixtureProvider{base: donor.Main}
		left, err := cache.Checkout(ctx, donorRepo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(left.Release)
		right, err := cache.Checkout(ctx, forkRepo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(right.Release)
		for _, tree := range []*git.Worktree{left, right} {
			Expect(tree.CreateBranch(ctx, "release/shared")).To(Succeed())
			Expect(tree.CreateTag(ctx, git.TagOptions{Name: "shared-v1", Message: "shared release", Tagger: fixtureSignature})).To(Succeed())
			Expect(tree.Push(ctx, git.PushOptions{Branch: "release/shared", Tags: []string{"shared-v1"}})).To(Succeed())
		}
		Expect(gitFixture(donor.Path, "rev-parse", "release/shared")).To(Equal(left.Ref.SHA))
		Expect(gitFixture(fork.Path, "rev-parse", "release/shared")).To(Equal(right.Ref.SHA))
		Expect(gitFixture(fork.Path, "cat-file", "tag", "shared-v1")).To(ContainSubstring("tag shared-v1\n"))
	})
	It("keeps shallow clones independent without shallowing the donor", func(ctx SpecContext) {
		clone := filepath.Join(fixtureTempDir(), "clone")
		_, err := git.Clone(ctx, forkRepo, git.CloneOptions{Cache: cache, Dir: clone, Ref: "main", Depth: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(gitFixture(clone, "rev-list", "--count", "HEAD")).To(Equal("1"))
		Expect(gitFixture(filepath.Join(cache.Root, donorRepo.URL.CacheKey(), "mirror.git"), "rev-parse", "--is-shallow-repository")).To(Equal("false"))
		Expect(provider.probes).To(BeZero())
	})
	It("deepens a legacy shallow mirror for a complete standalone clone", func(ctx SpecContext) {
		legacy := &git.Cache{Root: fixtureTempDir()}
		tree, err := legacy.Checkout(ctx, donorRepo, git.CheckoutOptions{Ref: "main", Depth: 1, Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(tree.Release()).To(Succeed())
		clone := filepath.Join(fixtureTempDir(), "complete")
		_, err = git.Clone(ctx, donorRepo, git.CloneOptions{Cache: legacy, Dir: clone, Ref: "main"})
		Expect(err).NotTo(HaveOccurred())
		Expect(gitFixture(clone, "rev-parse", "--is-shallow-repository")).To(Equal("false"))
		Expect(gitFixture(clone, "rev-list", "--count", "HEAD")).To(Equal("2"))
	})
	It("preserves the requested annotated tag in an independent clone", func(ctx SpecContext) {
		clone := filepath.Join(fixtureTempDir(), "tagged")
		_, err := git.Clone(ctx, donorRepo, git.CloneOptions{Cache: cache, Dir: clone, Ref: "v1"})
		Expect(err).NotTo(HaveOccurred())
		Expect(gitFixture(clone, "cat-file", "tag", "v1")).To(ContainSubstring("tag v1\n"))
		Expect(gitFixture(clone, "rev-parse", "v1^{commit}")).To(Equal(donor.First))
	})
	It("propagates probe failures without publishing a binding", func(ctx SpecContext) {
		provider.failure = errors.New("provider unavailable")
		_, err := cache.Checkout(ctx, forkRepo, git.CheckoutOptions{Ref: "main"})
		Expect(err).To(MatchError(ContainSubstring("provider unavailable")))
		Expect(filepath.Join(cache.Root, forkRepo.URL.CacheKey(), "repository.json")).NotTo(BeAnExistingFile())
	})
	It("uses a dedicated mirror for unrelated histories", func(ctx SpecContext) {
		provider.failure = git.ErrUnrelatedHistories
		tree, err := cache.Checkout(ctx, forkRepo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(tree.Release)
		Expect(filepath.Join(cache.Root, forkRepo.URL.CacheKey(), "mirror.git")).To(BeADirectory())
	})
	It("refuses a corrupt ownership record", func(ctx SpecContext) {
		path := filepath.Join(cache.Root, forkRepo.URL.CacheKey())
		Expect(os.MkdirAll(path, 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(path, "repository.json"), []byte(`{"version":1,"owner":"../escape"}`), 0o600)).To(Succeed())
		_, err := cache.Checkout(ctx, forkRepo, git.CheckoutOptions{Ref: "main"})
		Expect(err).To(MatchError(ContainSubstring("invalid cache binding")))
	})
	It("maintains a shared owner while a worktree lease is active", func(ctx SpecContext) {
		tree, err := cache.Checkout(ctx, forkRepo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(tree.Release)
		Expect(cache.Maintain(ctx, git.MaintainOptions{PinTTL: time.Nanosecond})).To(Succeed())
		Expect(gitFixture(tree.Dir, "show", "HEAD:fork.txt")).To(Equal("fork"))
		Expect(provider.probes).To(Equal(1))
	})
	It("serializes concurrent discovery and permits independent ephemeral leases", func(ctx SpecContext) {
		type result struct {
			tree *git.Worktree
			err  error
		}
		results := make(chan result, 2)
		for range 2 {
			go func() {
				tree, err := cache.Checkout(ctx, forkRepo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
				results <- result{tree, err}
			}()
		}
		for range 2 {
			var actual result
			Eventually(results).Should(Receive(&actual))
			Expect(actual.err).NotTo(HaveOccurred())
			Expect(actual.tree.Release()).To(Succeed())
		}
		Expect(provider.probes).To(Equal(1))
	})
	It("cancels discovery without publishing ownership", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := cache.Checkout(ctx, forkRepo, git.CheckoutOptions{Ref: fork.Main})
		Expect(err).To(HaveOccurred())
		Expect(filepath.Join(cache.Root, forkRepo.URL.CacheKey(), "repository.json")).NotTo(BeAnExistingFile())
	})
})
