package git_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("scoped object pool lifecycle", func() {
	var cache *git.Cache
	var donor, fork bareRepo
	var donorRepo, forkRepo *git.Repository
	var remote *httpRemote
	BeforeEach(func(ctx SpecContext) {
		donor, fork, remote = organizationFixtures()
		gitFixture(donor.Path, "config", "http.receivepack", "true")
		gitFixture(fork.Path, "config", "http.receivepack", "true")
		fork.commitOn(fixtureCommit{Branch: "main", Path: "fork.txt", Content: "fork\n", Message: "fork"})
		cache = &git.Cache{Root: fixtureTempDir()}
		donorRepo = organizationRepository(remote, donor, "example")
		tree, err := cache.Checkout(ctx, donorRepo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(tree.Release()).To(Succeed())
		forkRepo = organizationRepository(remote, fork, "example")
		forkRepo.Provider = &unavailableAncestry{}
	})
	It("reads updated refs after restart while keeping the donor's refs separate", func(ctx SpecContext) {
		_, err := cache.Show(ctx, forkRepo, fork.Main, "README.md")
		Expect(err).NotTo(HaveOccurred())
		tip := fork.commitOn(fixtureCommit{Branch: "main", Path: "later.txt", Content: "later\n", Message: "advance"})
		restarted := &git.Cache{Root: cache.Root}
		data, err := restarted.Show(ctx, forkRepo, tip, "later.txt")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal("later\n"))
		Expect(gitFixture(filepath.Join(cache.Root, "v2", donorRepo.URL.CacheKey(), "mirror.git"), "rev-parse",
			"refs/commons-cache/repos/"+donorRepo.URL.CacheKey()+"/heads/main")).To(Equal(donor.Main))
	})
	It("isolates equal branch and annotated tag names and pushes to the correct remote", func(ctx SpecContext) {
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
	It("keeps a shallow clone independent of the cache", func(ctx SpecContext) {
		clone := filepath.Join(fixtureTempDir(), "clone")
		_, err := git.Clone(ctx, forkRepo, git.CloneOptions{Cache: cache, Dir: clone, Ref: "main", Depth: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(gitFixture(clone, "rev-list", "--count", "HEAD")).To(Equal("1"))
		Expect(filepath.Join(clone, ".git", "objects", "info", "alternates")).NotTo(BeAnExistingFile())
		Expect(os.RemoveAll(cache.Root)).To(Succeed())
		gitFixture(clone, "fsck", "--full")
	})
	It("keeps the mirror complete after a shallow clone and serves a complete clone from it", func(ctx SpecContext) {
		fresh := &git.Cache{Root: fixtureTempDir()}
		_, err := git.Clone(ctx, donorRepo, git.CloneOptions{Cache: fresh, Dir: filepath.Join(fixtureTempDir(), "shallow"), Ref: "v1", Depth: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(gitFixture(filepath.Join(fresh.Root, "v2", donorRepo.URL.CacheKey(), "mirror.git"), "rev-parse", "--is-shallow-repository")).To(Equal("false"))
		clone := filepath.Join(fixtureTempDir(), "complete")
		_, err = git.Clone(ctx, donorRepo, git.CloneOptions{Cache: fresh, Dir: clone, Ref: "main"})
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
	It("fetches an unrelated history into its own mirror with the provider API unavailable", func(ctx SpecContext) {
		tip := fork.orphanOn("independent", "independent.txt", "independent")
		tree, err := cache.Checkout(ctx, forkRepo, git.CheckoutOptions{Ref: tip, Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(tree.Release)
		Expect(filepath.Join(cache.Root, "v2", forkRepo.URL.CacheKey(), "mirror.git")).To(BeADirectory())
	})
	It("leaves legacy bindings alone when populating a new mirror", func(ctx SpecContext) {
		path := filepath.Join(cache.Root, forkRepo.URL.CacheKey())
		Expect(os.MkdirAll(path, 0o700)).To(Succeed())
		binding := []byte(`{"version":1,"owner":"legacy-donor"}`)
		Expect(os.WriteFile(filepath.Join(path, "repository.json"), binding, 0o600)).To(Succeed())
		tree, err := cache.Checkout(ctx, forkRepo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(tree.Release)
		Expect(os.ReadFile(filepath.Join(path, "repository.json"))).To(Equal(binding))
	})
	It("maintains the pool while a worktree lease is active", func(ctx SpecContext) {
		tree, err := cache.Checkout(ctx, forkRepo, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(tree.Release)
		Expect(cache.Maintain(ctx, git.MaintainOptions{PinTTL: time.Nanosecond})).To(Succeed())
		Expect(gitFixture(tree.Dir, "show", "HEAD:fork.txt")).To(Equal("fork"))
	})
	It("serializes writes to a mirror and permits independent ephemeral leases", func(ctx SpecContext) {
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
			Eventually(ctx, results).WithTimeout(30 * time.Second).Should(Receive(&actual))
			Expect(actual.err).NotTo(HaveOccurred())
			Expect(actual.tree.Release()).To(Succeed())
		}
	})
	It("cancels checkout before publishing mirror metadata", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := cache.Checkout(ctx, forkRepo, git.CheckoutOptions{Ref: fork.Main})
		Expect(err).To(HaveOccurred())
		Expect(filepath.Join(cache.Root, "v2", forkRepo.URL.CacheKey(), "repository.json")).NotTo(BeAnExistingFile())
	})
})
