package git_test

import (
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Cache.Log and Cache.Compare", func() {
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

	summary := func(commits []git.Commit) []git.Commit {
		out := make([]git.Commit, len(commits))
		for i, commit := range commits {
			Expect(commit.ShortSHA).ToNot(BeEmpty())
			Expect(commit.SHA).To(HavePrefix(commit.ShortSHA))
			Expect(commit.Date.IsZero()).To(BeFalse())
			out[i] = git.Commit{SHA: commit.SHA, Subject: commit.Subject, Body: commit.Body,
				Author: commit.Author, Parents: commit.Parents, Files: commit.Files}
		}
		return out
	}

	It("lists the commits on head not on base oldest first, with files and merge parents", func(ctx SpecContext) {
		side := fixture.commitOn("side", fixture.Main, "side.txt", "side\n", "side work\n\nwhy it matters")
		merge := fixture.mergeInto("feature", "side")
		author := git.Signature{Name: "Fixture", Email: "fixture@example.org"}

		commits, err := cache.Log(ctx, repo, "main", "feature", git.LogOptions{})
		Expect(err).ToNot(HaveOccurred())
		Expect(summary(commits)).To(Equal([]git.Commit{
			{SHA: fixture.Feature, Subject: "feature", Author: author, Parents: []string{fixture.Main}, Files: []string{"feature.txt"}},
			{SHA: side, Subject: "side work", Body: "why it matters", Author: author, Parents: []string{fixture.Main}, Files: []string{"side.txt"}},
			{SHA: merge, Subject: "merge side", Author: author, Parents: []string{fixture.Feature, side}},
		}))
	})

	It("keeps the newest Limit commits and filters by path", func(ctx SpecContext) {
		second := fixture.commitOn("feature", "", "other.txt", "x\n", "other")
		third := fixture.commitOn("feature", "", "feature.txt", "changed\n", "feature again")

		limited, err := cache.Log(ctx, repo, "main", "feature", git.LogOptions{Limit: 2})
		Expect(err).ToNot(HaveOccurred())
		Expect([]string{limited[0].SHA, limited[1].SHA}).To(Equal([]string{second, third}))

		scoped, err := cache.Log(ctx, repo, "main", "feature", git.LogOptions{Paths: []string{"feature.txt"}})
		Expect(err).ToNot(HaveOccurred())
		Expect([]string{scoped[0].SHA, scoped[1].SHA}).To(Equal([]string{fixture.Feature, third}))
		Expect(scoped).To(HaveLen(2))
	})

	It("compares two refs through a shallow mirror and sees a base that advanced since the last fetch", func(ctx SpecContext) {
		shallow, err := cache.Checkout(ctx, repo, git.CheckoutOptions{Ref: "feature", Depth: 1})
		Expect(err).ToNot(HaveOccurred())
		Expect(shallow.Release()).To(Succeed())

		comparison, err := cache.Compare(ctx, repo, "main", "feature")
		Expect(err).ToNot(HaveOccurred())
		Expect(comparison).To(Equal(git.Comparison{Ahead: 1, Behind: 0, MergeBase: fixture.Main}))

		fixture.commitOn("main", "", "hotfix.txt", "fix\n", "hotfix")
		comparison, err = cache.Compare(ctx, repo, "main", "feature")
		Expect(err).ToNot(HaveOccurred())
		Expect(comparison).To(Equal(git.Comparison{Ahead: 1, Behind: 1, MergeBase: fixture.Main}))
	})

	It("refuses an unknown ref", func(ctx SpecContext) {
		_, err := cache.Log(ctx, repo, "main", "missing", git.LogOptions{})
		Expect(err).To(MatchError(ContainSubstring(`ref "missing" not found`)))
	})
})
