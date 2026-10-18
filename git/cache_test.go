package git_test

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var fixtureSignature = git.Signature{Name: "Apply Bot", Email: "apply@example.org"}

var _ = Describe("Cache", func() {
	var (
		fixture bareRepo
		cache   *git.Cache
		repo    *git.Repository
	)
	BeforeEach(func() {
		fixture = newBareRepo("rules")
		cache = &git.Cache{Root: GinkgoT().TempDir()}
		repo = newRepository(fixture.FileURL(), git.Auth{})
	})

	checkout := func(ctx context.Context, options git.CheckoutOptions) *git.Worktree {
		GinkgoHelper()
		worktree, err := cache.Checkout(ctx, repo, options)
		Expect(err).ToNot(HaveOccurred())
		return worktree
	}

	It("checks out a branch, a tag and a raw SHA", func(ctx SpecContext) {
		for ref, sha := range map[string]string{"feature": fixture.Feature, "v1-lite": fixture.First, fixture.Main: fixture.Main} {
			worktree := checkout(ctx, git.CheckoutOptions{Ref: ref, Depth: 1})
			Expect(worktree.Ref.SHA).To(Equal(sha))
			Expect(gitFixture(worktree.Dir, "rev-parse", "HEAD")).To(Equal(sha))
			Expect(worktree.Release()).To(Succeed())
		}
	})

	It("holds leases on different refs concurrently and blocks a second lease on the same ref", func(ctx SpecContext) {
		main := checkout(ctx, git.CheckoutOptions{Ref: "main"})
		feature := checkout(ctx, git.CheckoutOptions{Ref: "feature"})
		Expect(main.Dir).ToNot(Equal(feature.Dir))

		short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		_, err := cache.Checkout(short, repo, git.CheckoutOptions{Ref: "main"})
		Expect(err).To(MatchError(ContainSubstring("lease")))

		Expect(main.Release()).To(Succeed())
		again := checkout(ctx, git.CheckoutOptions{Ref: "main"})
		Expect(again.Dir).To(Equal(main.Dir))
		Expect(again.Release()).To(Succeed())
		Expect(feature.Release()).To(Succeed())
	})

	It("removes an ephemeral worktree on release", func(ctx SpecContext) {
		worktree := checkout(ctx, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		Expect(worktree.Dir).To(BeADirectory())
		Expect(worktree.Release()).To(Succeed())
		Expect(worktree.Dir).ToNot(BeAnExistingFile())
	})

	It("makes no fetch when the commit is already cached", func(ctx SpecContext) {
		remote := newHTTPRemote(fixture.Root, false)
		repo = newRepository(remote.URL(fixture), git.Auth{Username: remote.User, Password: remote.Password})
		Expect(checkout(ctx, git.CheckoutOptions{Ref: "main"}).Release()).To(Succeed())
		fetches := remote.uploadPacks()
		Expect(fetches).To(BeNumerically(">", 0))
		Expect(checkout(ctx, git.CheckoutOptions{Ref: "main", Ephemeral: true}).Release()).To(Succeed())
		Expect(remote.uploadPacks()).To(Equal(fetches))

		mirrorConfig, err := os.ReadFile(filepath.Join(cache.Root, repo.URL.CacheKey(), "mirror.git", "config"))
		Expect(err).ToNot(HaveOccurred())
		Expect(string(mirrorConfig)).ToNot(ContainSubstring(remote.Password))
		Expect(string(mirrorConfig)).ToNot(ContainSubstring(base64.StdEncoding.EncodeToString([]byte(remote.User + ":" + remote.Password))))
	})

	It("commits, tags and pushes a branch with push options", func(ctx SpecContext) {
		record := fixture.installPushOptionHook()
		worktree := checkout(ctx, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		DeferCleanup(worktree.Release)
		Expect(worktree.CreateBranch(ctx, "apply/one")).To(Succeed())
		Expect(worktree.WriteFile("rules/new.xml", []byte("<rule/>"))).To(Succeed())
		Expect(worktree.DeleteFile("README.md")).To(Succeed())
		sha, err := worktree.Commit(ctx, git.CommitOptions{Message: "apply one", Author: fixtureSignature})
		Expect(err).ToNot(HaveOccurred())
		Expect(worktree.CreateTag(ctx, git.TagOptions{Name: "apply-1", Message: "applied", Tagger: fixtureSignature})).To(Succeed())
		Expect(worktree.Push(ctx, git.PushOptions{Branch: "apply/one", Tags: []string{"apply-1"},
			ServerOptions: []string{"ci.skip", "topic=apply"}})).To(Succeed())

		Expect(gitFixture(fixture.Path, "rev-parse", "refs/heads/apply/one")).To(Equal(sha))
		Expect(gitFixture(fixture.Path, "rev-parse", "refs/tags/apply-1^{commit}")).To(Equal(sha))
		Expect(gitFixture(fixture.Path, "log", "-1", "--format=%an <%ae>|%s", "apply/one")).To(Equal("Apply Bot <apply@example.org>|apply one"))
		Expect(gitFixture(fixture.Path, "ls-tree", "--name-only", "-r", "apply/one")).To(Equal("rules/new.xml"))
		Expect(os.ReadFile(record)).To(BeEquivalentTo("ci.skip\ntopic=apply\n"))
	})

	It("rejects a non-fast-forward push unless forced with a lease", func(ctx SpecContext) {
		worktree := checkout(ctx, git.CheckoutOptions{Ref: "feature", Ephemeral: true})
		DeferCleanup(worktree.Release)
		Expect(worktree.CreateBranch(ctx, "rewrite")).To(Succeed())
		gitFixture(worktree.Dir, "reset", "-q", "--hard", fixture.Main)
		Expect(worktree.WriteFile("other.txt", []byte("x"))).To(Succeed())
		sha, err := worktree.Commit(ctx, git.CommitOptions{Message: "diverge", Author: fixtureSignature})
		Expect(err).ToNot(HaveOccurred())

		err = worktree.Push(ctx, git.PushOptions{Branch: "rewrite", RemoteBranch: "feature"})
		Expect(err).To(MatchError(ContainSubstring("rejected")))
		Expect(worktree.Push(ctx, git.PushOptions{Branch: "rewrite", RemoteBranch: "feature", Force: true,
			ExpectedSHA: fixture.Feature})).To(Succeed())
		Expect(gitFixture(fixture.Path, "rev-parse", "refs/heads/feature")).To(Equal(sha))
	})

	It("refuses paths escaping the worktree and deleting missing files", func(ctx SpecContext) {
		worktree := checkout(ctx, git.CheckoutOptions{Ref: "main", Ephemeral: true})
		DeferCleanup(worktree.Release)
		for _, path := range []string{"../escape", "/etc/passwd", ".git/config", "a/../../b"} {
			Expect(worktree.WriteFile(path, []byte("x"))).To(HaveOccurred(), path)
		}
		Expect(worktree.DeleteFile("missing.txt")).To(MatchError(ContainSubstring("missing.txt")))
	})
})
