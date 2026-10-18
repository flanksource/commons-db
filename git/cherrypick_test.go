package git_test

import (
	"errors"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Cache.Apply with CherryPick", func() {
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

	promote := func(shas ...string) git.ChangeRequest {
		return git.ChangeRequest{Base: "main", BaseSHA: fixture.Main, Branch: "promote/one", Message: "promote",
			Author: fixtureSignature, CherryPick: shas}
	}
	branchExists := func() string { return gitFixture(fixture.Path, "branch", "--list", "promote/one") }

	It("cherry-picks commits in order onto the base before the files, recording their origin", func(ctx SpecContext) {
		second := fixture.commitOn("feature", "", "other.txt", "x\n", "other")
		change := promote(fixture.Feature, second)
		change.Files = []git.FileChange{{Path: "release.txt", Content: []byte("r1\n")}}

		result, err := cache.Apply(ctx, repo, change)
		Expect(err).ToNot(HaveOccurred())
		Expect(gitFixture(fixture.Path, "log", "--format=%s", fixture.Main+".."+result.CommitSHA)).To(Equal("promote\nother\nfeature"))
		Expect(gitFixture(fixture.Path, "log", "-1", "--format=%b", result.CommitSHA+"^")).To(ContainSubstring("cherry picked from commit " + second))
		Expect(gitFixture(fixture.Path, "log", "-1", "--format=%an|%cn", result.CommitSHA+"^")).To(Equal("Fixture|Apply Bot"))
		Expect(gitFixture(fixture.Path, "ls-tree", "--name-only", "-r", "promote/one")).To(Equal("README.md\nfeature.txt\nother.txt\nrelease.txt"))
	})

	It("aborts a conflicting cherry-pick and pushes nothing", func(ctx SpecContext) {
		conflict := fixture.commitOn("conflict", fixture.First, "README.md", "conflict\n", "conflicting readme")
		_, err := cache.Apply(ctx, repo, promote(conflict))
		var conflictErr *git.CherryPickConflictError
		Expect(errors.As(err, &conflictErr)).To(BeTrue(), "%v", err)
		Expect(*conflictErr).To(Equal(git.CherryPickConflictError{Commit: conflict, Files: []string{"README.md"}}))
		Expect(branchExists()).To(BeEmpty())
	})

	It("refuses a merge commit without a mainline before pushing", func(ctx SpecContext) {
		fixture.commitOn("side", fixture.Main, "side.txt", "side\n", "side")
		merge := fixture.mergeInto("feature", "side")
		_, err := cache.Apply(ctx, repo, promote(merge))
		Expect(err).To(MatchError(And(ContainSubstring(merge), ContainSubstring("mainline"))))
		Expect(branchExists()).To(BeEmpty())

		change := promote(merge)
		change.CherryPickMainline = 1
		result, err := cache.Apply(ctx, repo, change)
		Expect(err).ToNot(HaveOccurred())
		Expect(gitFixture(fixture.Path, "ls-tree", "--name-only", "-r", result.CommitSHA)).To(Equal("README.md\nside.txt"))
	})

	It("appends the trailers to every picked commit's message, after its origin", func(ctx SpecContext) {
		second := fixture.commitOn("feature", "", "other.txt", "x\n", "other")
		change := promote(fixture.Feature, second)
		change.Trailers = []string{"Requested-by: ada"}

		result, err := cache.Apply(ctx, repo, change)
		Expect(err).ToNot(HaveOccurred())
		for _, rev := range []string{result.CommitSHA, result.CommitSHA + "^"} {
			Expect(gitFixture(fixture.Path, "log", "-1", "--format=%(trailers:key=Requested-by,valueonly)%an|%cn", rev)).To(Equal("ada\nFixture|Apply Bot"))
		}
		Expect(gitFixture(fixture.Path, "log", "-1", "--format=%b", result.CommitSHA)).To(ContainSubstring("cherry picked from commit " + second))
	})

	It("refuses a trailer that is not one Key: value line, before pushing", func(ctx SpecContext) {
		for _, trailer := range []string{"Requested-by ada", "Requested-by: ada\nSigned-off-by: eve", ": ada"} {
			change := promote(fixture.Feature)
			change.Trailers = []string{trailer}
			_, err := cache.Apply(ctx, repo, change)
			Expect(err).To(MatchError(ContainSubstring("trailer")), "trailer %q", trailer)
		}
		Expect(branchExists()).To(BeEmpty())
	})

	It("refuses an already applied commit unless AllowEmpty skips it", func(ctx SpecContext) {
		_, err := cache.Apply(ctx, repo, promote(fixture.Main))
		var empty *git.CherryPickEmptyError
		Expect(errors.As(err, &empty)).To(BeTrue(), "%v", err)
		Expect(*empty).To(Equal(git.CherryPickEmptyError{Commit: fixture.Main, Base: "main"}))
		Expect(err).To(MatchError(ContainSubstring("set AllowEmpty")))
		Expect(branchExists()).To(BeEmpty())

		change := promote(fixture.Main, fixture.Feature)
		change.AllowEmpty = true
		result, err := cache.Apply(ctx, repo, change)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.SkippedCherryPicks).To(Equal([]string{fixture.Main}))
		Expect(gitFixture(fixture.Path, "log", "--format=%s", fixture.Main+".."+result.CommitSHA)).To(Equal("feature"))
	})
})
