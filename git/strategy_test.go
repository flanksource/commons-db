package git_test

import (
	"strings"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// commitAs commits path=content on the checked-out branch of b's work
// checkout as author, at date, pushes it and returns the commit SHA.
func (b bareRepo) commitAs(branch, author, date, path, content, message string) string {
	GinkgoHelper()
	gitFixture(b.work(), "checkout", "-q", branch)
	writeFixtureFile(b.work(), path, content)
	gitFixture(b.work(), "add", "-A")
	gitFixture(b.work(), "commit", "-q", "--author", author, "--date", date, "-m", message)
	gitFixture(b.work(), "push", "-q", b.Path, branch)
	return gitFixture(b.work(), "rev-parse", "HEAD")
}

// commitFacts is what promoting a commit must keep: its message, author and authored date.
func commitFacts(repo, ref string) []string {
	GinkgoHelper()
	return strings.Split(gitFixture(repo, "show", "-s", "--format=%B%x00%an <%ae>%x00%aI%x00%P", ref), "\x00")
}

var _ = Describe("change strategies", func() {
	var (
		upstream, fork bareRepo
		cache          *git.Cache
		repo, forkRepo *git.Repository
	)
	const (
		thandi = "Thandi Dlamini <thandi@example.org>"
		sipho  = "Sipho Nkosi <sipho@example.org>"
	)
	BeforeEach(func() {
		upstream = newBareRepo("rules")
		fork = newFork(upstream)
		cache = &git.Cache{Root: GinkgoT().TempDir()}
		var err error
		repo, err = git.NewRepository(git.RepositoryOptions{URL: upstream.FileURL(), Provider: &recordingProvider{}, ProviderType: models.ConnectionTypeGit})
		Expect(err).ToNot(HaveOccurred())
		forkRepo = newRepository(fork.FileURL(), git.Auth{})
	})

	Describe("Cache.MergeBase", func() {
		It("finds the common ancestor of a fork branch and the base", func(ctx SpecContext) {
			fork.commitOn("main", "", "fork.txt", "fork\n", "fork work")
			Expect(cache.MergeBase(ctx, repo, "main", "main", git.MergeBaseOptions{HeadFrom: forkRepo})).To(Equal(upstream.Main))
		})

		It("refuses branches that share no history", func(ctx SpecContext) {
			fork.orphanOn("lone", "lone.txt", "lone\n")
			_, err := cache.MergeBase(ctx, repo, "main", "lone", git.MergeBaseOptions{HeadFrom: forkRepo})
			Expect(err).To(MatchError(git.ErrUnrelatedHistories))
		})
	})

	It("lists the commits of an unrelated fork branch by patch, for a replay", func(ctx SpecContext) {
		lone := fork.orphanOn("lone", "lone.txt", "lone\n")
		commits, err := cache.Log(ctx, repo, "main", "lone", git.LogOptions{HeadFrom: forkRepo})
		Expect(err).ToNot(HaveOccurred())
		Expect(commits).To(HaveLen(1))
		Expect(commits[0].SHA).To(Equal(lone))
	})

	It("replays commits of an unrelated branch keeping each one's message, author and date", func(ctx SpecContext) {
		fork.orphanOn("lone", "lone.txt", "lone\n")
		first := fork.commitAs("lone", thandi, "2026-10-17T08:00:00+02:00", "rules/a.xml", "<a/>\n", "Add rule A\n\nBecause underwriting asked.")
		second := fork.commitAs("lone", sipho, "2026-10-17T09:00:00+02:00", "rules/b.xml", "<b/>\n", "Add rule B")

		result, err := cache.Apply(ctx, repo, git.ChangeRequest{Base: "main", BaseSHA: upstream.Main, Branch: "promote/replay",
			Author: fixtureSignature, Source: forkRepo, Pick: git.PickReplay, CherryPick: []string{first, second}, Trailers: []string{"Applied-by: ada"}})
		Expect(err).ToNot(HaveOccurred())
		Expect([][]string{commitFacts(upstream.Path, result.CommitSHA+"^"), commitFacts(upstream.Path, result.CommitSHA)}).To(Equal([][]string{
			{"Add rule A\n\nBecause underwriting asked.\n\nApplied-by: ada\n", thandi, "2026-10-17T08:00:00+02:00", upstream.Main},
			{"Add rule B\n\nApplied-by: ada\n", sipho, "2026-10-17T09:00:00+02:00", gitFixture(upstream.Path, "rev-parse", result.CommitSHA+"^")},
		}))
		Expect(gitFixture(upstream.Path, "show", "promote/replay:rules/b.xml")).To(Equal("<b/>"))
	})

	It("squashes picked commits into one, keeping the messages, the first author and the others as co-authors", func(ctx SpecContext) {
		first := fork.commitAs("main", thandi, "2026-10-17T08:00:00+02:00", "rules/a.xml", "<a/>\n", "Add rule A")
		second := fork.commitAs("main", sipho, "2026-10-17T09:00:00+02:00", "rules/b.xml", "<b/>\n", "Add rule B\n\nWith a body.")

		result, err := cache.Apply(ctx, repo, git.ChangeRequest{Base: "main", BaseSHA: upstream.Main, Branch: "promote/squash",
			Author: fixtureSignature, Source: forkRepo, CherryPick: []string{first, second}, Squash: true, Trailers: []string{"Applied-by: ada"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(commitFacts(upstream.Path, result.CommitSHA)).To(Equal([]string{
			"Add rule A\n\nAdd rule B\n\nWith a body.\n\nCo-authored-by: " + sipho + "\nApplied-by: ada\n",
			thandi, "2026-10-17T08:00:00+02:00", upstream.Main,
		}))
		Expect(gitFixture(upstream.Path, "ls-tree", "--name-only", "-r", "promote/squash", "rules")).To(Equal("rules/a.xml\nrules/b.xml"))
	})

	It("rebases a fork branch onto the advanced base, linear, keeping authors and messages and adding no origin line", func(ctx SpecContext) {
		fork.commitAs("main", thandi, "2026-10-17T08:00:00+02:00", "rules/a.xml", "<a/>\n", "Add rule A")
		head := fork.commitAs("main", sipho, "2026-10-17T09:00:00+02:00", "rules/b.xml", "<b/>\n", "Add rule B")
		advanced := upstream.commitOn("main", "", "upstream.txt", "upstream\n", "upstream work")

		result, err := cache.Apply(ctx, repo, git.ChangeRequest{Base: "main", BaseSHA: advanced, Branch: "promote/rebase",
			Author: fixtureSignature, Source: forkRepo, Rebase: head})
		Expect(err).ToNot(HaveOccurred())
		Expect([][]string{commitFacts(upstream.Path, result.CommitSHA+"^"), commitFacts(upstream.Path, result.CommitSHA)}).To(Equal([][]string{
			{"Add rule A\n", thandi, "2026-10-17T08:00:00+02:00", advanced},
			{"Add rule B\n", sipho, "2026-10-17T09:00:00+02:00", gitFixture(upstream.Path, "rev-parse", result.CommitSHA+"^")},
		}))
	})

	It("refuses to rebase a branch sharing no history with the base before pushing", func(ctx SpecContext) {
		lone := fork.orphanOn("lone", "lone.txt", "lone\n")
		_, err := cache.Apply(ctx, repo, git.ChangeRequest{Base: "main", Branch: "promote/x", Author: fixtureSignature, Source: forkRepo, Rebase: lone})
		Expect(err).To(MatchError(git.ErrUnrelatedHistories))
		Expect(gitFixture(upstream.Path, "branch", "--list", "promote/x")).To(BeEmpty())
	})

	DescribeTable("refuses a strategy it cannot carry out",
		func(ctx SpecContext, change git.ChangeRequest, want string) {
			change.Base, change.Branch, change.Author = "main", "promote/x", fixtureSignature
			_, err := cache.Apply(ctx, repo, change)
			Expect(err).To(MatchError(ContainSubstring(want)))
		},
		Entry("a rebase with commits to cherry-pick", git.ChangeRequest{Rebase: strings.Repeat("a", 40), CherryPick: []string{strings.Repeat("b", 40)}},
			"a rebase takes no files, commits to cherry-pick or head"),
		Entry("a rebase with trailers", git.ChangeRequest{Rebase: strings.Repeat("a", 40), Trailers: []string{"Applied-by: ada"}},
			"a rebase keeps each commit's message: it takes no trailers"),
		Entry("a squash without commits", git.ChangeRequest{Squash: true, Files: []git.FileChange{{Path: "a", Content: []byte("a")}}},
			"squash folds the commits to cherry-pick"),
		Entry("an unknown pick strategy", git.ChangeRequest{Pick: "octopus", CherryPick: []string{strings.Repeat("b", 40)}},
			`pick strategy "octopus"`),
	)
})
