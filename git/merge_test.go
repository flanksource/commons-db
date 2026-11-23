package git_test

import (
	"errors"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Cache.Apply with Merge", func() {
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

	stack := func(merges ...git.MergeCommit) git.ChangeRequest {
		return git.ChangeRequest{Base: "main", BaseSHA: fixture.Main, Branch: "stack/one", Author: fixtureSignature, Merge: merges}
	}
	branchExists := func() string { return gitFixture(fixture.Path, "branch", "--list", "stack/one") }

	It("merges each head in order onto the base with its own merge commit and pushes the result", func(ctx SpecContext) {
		other := fixture.commitOn(fixtureCommit{Branch: "other", From: fixture.Main, Path: "other.txt", Content: "o\n", Message: "other"})

		result, err := cache.Apply(ctx, repo, stack(git.MergeCommit{SHA: fixture.Feature, Message: "Merge #1"}, git.MergeCommit{SHA: other}))
		Expect(err).ToNot(HaveOccurred())
		Expect(result.CommitSHA).To(Equal(gitFixture(fixture.Path, "rev-parse", "stack/one")))
		Expect(gitFixture(fixture.Path, "log", "--first-parent", "--format=%s|%cn", fixture.Main+".."+result.CommitSHA)).
			To(Equal("Merge " + other + "|Apply Bot\nMerge #1|Apply Bot"))
		Expect(gitFixture(fixture.Path, "rev-parse", result.CommitSHA+"^2", result.CommitSHA+"^1^2")).To(Equal(other + "\n" + fixture.Feature))
		Expect(gitFixture(fixture.Path, "ls-tree", "--name-only", "-r", "stack/one")).To(Equal("README.md\nfeature.txt\nother.txt"))
	})

	It("aborts a conflicting merge, names the head and its files, and pushes nothing", func(ctx SpecContext) {
		conflict := fixture.commitOn(fixtureCommit{Branch: "conflict", From: fixture.First, Path: "README.md", Content: "conflict\n", Message: "conflicting readme"})

		_, err := cache.Apply(ctx, repo, stack(git.MergeCommit{SHA: fixture.Feature}, git.MergeCommit{SHA: conflict}))
		var conflictErr *git.MergeConflictError
		Expect(errors.As(err, &conflictErr)).To(BeTrue(), "%v", err)
		Expect(*conflictErr).To(Equal(git.MergeConflictError{Commit: conflict, Files: []string{"README.md"}}))
		Expect(branchExists()).To(BeEmpty())
	})

	It("names a conflicting file exactly, leading space and tab included", func(ctx SpecContext) {
		const odd = " rules\tgl.yaml"
		first := fixture.commitOn(fixtureCommit{Branch: "odd-a", From: fixture.Main, Path: odd, Content: "a\n", Message: "odd a"})
		second := fixture.commitOn(fixtureCommit{Branch: "odd-b", From: fixture.Main, Path: odd, Content: "b\n", Message: "odd b"})

		_, err := cache.Apply(ctx, repo, stack(git.MergeCommit{SHA: first}, git.MergeCommit{SHA: second}))
		var conflictErr *git.MergeConflictError
		Expect(errors.As(err, &conflictErr)).To(BeTrue(), "%v", err)
		Expect(conflictErr.Files).To(Equal([]string{odd}))
	})

	It("refuses a head already on the base before pushing", func(ctx SpecContext) {
		_, err := cache.Apply(ctx, repo, stack(git.MergeCommit{SHA: fixture.First}))
		Expect(err).To(MatchError(And(ContainSubstring(fixture.First), ContainSubstring("already on main"))))
		Expect(branchExists()).To(BeEmpty())
	})

	DescribeTable("refuses a merge combined with another way of landing commits",
		func(ctx SpecContext, change func(*git.ChangeRequest), problem string) {
			request := stack(git.MergeCommit{SHA: fixture.Feature})
			change(&request)
			_, err := cache.Apply(ctx, repo, request)
			Expect(err).To(MatchError(ContainSubstring(problem)))
		},
		Entry("cherry-picks", func(r *git.ChangeRequest) { r.CherryPick = []string{fixture.Feature} }, "a merge takes no"),
		Entry("files", func(r *git.ChangeRequest) { r.Files = []git.FileChange{{Path: "x", Content: []byte("x")}} }, "a merge takes no"),
		Entry("a short SHA", func(r *git.ChangeRequest) { r.Merge = []git.MergeCommit{{SHA: "abc123"}} }, "full 40-character"),
	)
})

var _ = Describe("Cache.DeleteBranch", func() {
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

	It("deletes a remote branch still at the expected commit", func(ctx SpecContext) {
		Expect(cache.DeleteBranch(ctx, repo, "feature", fixture.Feature)).To(Succeed())
		Expect(gitFixture(fixture.Path, "branch", "--list", "feature")).To(BeEmpty())
	})

	It("keeps a branch that moved since and reports it", func(ctx SpecContext) {
		Expect(cache.DeleteBranch(ctx, repo, "feature", fixture.Main)).To(MatchError(ContainSubstring("feature")))
		Expect(gitFixture(fixture.Path, "rev-parse", "feature")).To(Equal(fixture.Feature))
	})
})
