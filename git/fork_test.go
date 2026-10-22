package git_test

import (
	"path/filepath"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// newFork is a bare clone of upstream with its own work checkout, so commits
// pushed to it diverge from upstream as a fork's do.
func newFork(upstream bareRepo) bareRepo {
	GinkgoHelper()
	root := GinkgoT().TempDir()
	bare := filepath.Join(root, "fork.git")
	gitFixture(root, "clone", "-q", "--bare", upstream.Path, bare)
	gitFixture(root, "clone", "-q", bare, "work")
	return bareRepo{Root: root, Path: bare, Main: upstream.Main, First: upstream.First, Feature: upstream.Feature}
}

// orphanOn commits path=content on a new branch with no parent, sharing no
// history with anything else, pushes it and returns its SHA.
func (b bareRepo) orphanOn(branch, path, content string) string {
	GinkgoHelper()
	gitFixture(b.work(), "checkout", "-q", "--orphan", branch)
	gitFixture(b.work(), "rm", "-rq", "--cached", ".")
	writeFixtureFile(b.work(), path, content)
	gitFixture(b.work(), "add", path)
	gitFixture(b.work(), "commit", "-q", "-m", "unrelated "+branch)
	gitFixture(b.work(), "push", "-q", b.Path, branch)
	return gitFixture(b.work(), "rev-parse", "HEAD")
}

var _ = Describe("a fork of the changed repository", func() {
	var (
		upstream, fork bareRepo
		cache          *git.Cache
		provider       *recordingProvider
		repo, forkRepo *git.Repository
	)
	BeforeEach(func() {
		upstream = newBareRepo("rules")
		fork = newFork(upstream)
		cache = &git.Cache{Root: GinkgoT().TempDir()}
		provider = &recordingProvider{}
		var err error
		repo, err = git.NewRepository(git.RepositoryOptions{URL: upstream.FileURL(), Provider: provider, ProviderType: models.ConnectionTypeGit})
		Expect(err).ToNot(HaveOccurred())
		forkRepo = newRepository(fork.FileURL(), git.Auth{})
	})

	mirrorRef := func(owner *git.Repository, ref string) string {
		GinkgoHelper()
		return gitFixture(filepath.Join(cache.Root, owner.URL.CacheKey(), "mirror.git"), "rev-parse", "--verify", ref)
	}

	It("logs the fork's commits the repository lacks, keeping the fork's refs out of origin", func(ctx SpecContext) {
		ahead := fork.commitOn(fixtureCommit{Branch: "main", Path: "fork.txt", Content: "fork\n", Message: "fork work"})
		upstreamAhead := upstream.commitOn(fixtureCommit{Branch: "main", Path: "upstream.txt", Content: "upstream\n", Message: "upstream work"})

		commits, err := cache.Log(ctx, repo, git.LogOptions{Base: "main", Head: "main", HeadFrom: forkRepo})
		Expect(err).ToNot(HaveOccurred())
		Expect(commits).To(HaveLen(1))
		Expect(commits[0].SHA).To(Equal(ahead))
		Expect(commits[0].Files).To(Equal([]string{"fork.txt"}))
		Expect(mirrorRef(repo, "refs/remotes/origin/main")).To(Equal(upstreamAhead))
		Expect(mirrorRef(repo, "refs/remotes/"+forkRepo.URL.CacheKey()+"/main")).To(Equal(ahead))
	})

	It("cherry-picks a fork commit onto a branch of the repository", func(ctx SpecContext) {
		ahead := fork.commitOn(fixtureCommit{Branch: "main", Path: "fork.txt", Content: "fork\n", Message: "fork work"})

		result, err := cache.Apply(ctx, repo, git.ChangeRequest{Base: "main", BaseSHA: upstream.Main, Branch: "promote/1",
			Author: fixtureSignature, Source: forkRepo, CherryPick: []string{ahead}})
		Expect(err).ToNot(HaveOccurred())
		Expect(gitFixture(upstream.Path, "rev-parse", "promote/1^")).To(Equal(upstream.Main))
		Expect(gitFixture(upstream.Path, "show", "-s", "--format=%s", result.CommitSHA)).To(Equal("fork work"))
		Expect(gitFixture(upstream.Path, "show", "promote/1:fork.txt")).To(Equal("fork"))
	})

	It("pushes a fork head as a branch of the repository and opens a pull request into the base", func(ctx SpecContext) {
		ahead := fork.commitOn(fixtureCommit{Branch: "main", Path: "fork.txt", Content: "fork\n", Message: "fork work"})

		result, err := cache.Apply(ctx, repo, git.ChangeRequest{Base: "main", BaseSHA: upstream.Main, Branch: "promote/fork-main",
			Source: forkRepo, Head: ahead, PullRequest: &git.PullRequestRequest{Title: "Promote fork"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.CommitSHA).To(Equal(ahead))
		Expect(gitFixture(upstream.Path, "rev-parse", "refs/heads/promote/fork-main")).To(Equal(ahead))
		Expect(provider.opened).To(Equal([]git.PullRequestRequest{{Title: "Promote fork", Head: "promote/fork-main", Base: "main"}}))
	})

	DescribeTable("refuses a head it cannot promote before pushing",
		func(ctx SpecContext, head func() string, want any) {
			_, err := cache.Apply(ctx, repo, git.ChangeRequest{Base: "main", Branch: "promote/x", Source: forkRepo, Head: head()})
			Expect(err).To(MatchError(want))
			Expect(gitFixture(upstream.Path, "branch", "--list", "promote/x")).To(BeEmpty())
		},
		Entry("a head with no history in common", func() string { return fork.orphanOn("lone", "lone.txt", "lone\n") }, git.ErrUnrelatedHistories),
		Entry("a head already on the base", func() string { return upstream.First }, ContainSubstring("already on main")),
	)

	It("refuses a head alongside files or cherry-picks", func(ctx SpecContext) {
		_, err := cache.Apply(ctx, repo, git.ChangeRequest{Base: "main", Branch: "promote/x", Head: upstream.Feature,
			CherryPick: []string{upstream.Feature}})
		Expect(err).To(MatchError(ContainSubstring("takes no files and no commits")))
	})
})
