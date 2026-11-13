package git_test

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CherryPickLocal", func() {
	var fixture bareRepo
	BeforeEach(func() {
		fixture = newBareRepo("rules")
		GinkgoT().Setenv("GIT_COMMITTER_NAME", fixtureSignature.Name)
		GinkgoT().Setenv("GIT_COMMITTER_EMAIL", fixtureSignature.Email)
	})
	pick := func(shas ...string) git.LocalCherryPickOptions {
		return git.LocalCherryPickOptions{Dir: fixture.Path, Base: fixture.Main, CherryPick: shas}
	}
	worktrees := func() string { return gitFixture(fixture.Path, "worktree", "list", "--porcelain") }

	It("replays commits in order onto the base without a worktree, keeping message, author and date", func(ctx SpecContext) {
		second := fixture.commitOn(fixtureCommit{Branch: "feature", Path: "other.txt", Content: "x\n", Message: "other\n\nwhy it matters"})
		before := worktrees()

		tip, err := git.CherryPickLocal(ctx, pick(fixture.Feature, second))
		Expect(err).ToNot(HaveOccurred())
		Expect(gitFixture(fixture.Path, "log", "--format=%s", fixture.Main+".."+tip)).To(Equal("other\nfeature"))
		original := gitFixture(fixture.Path, "log", "-1", "--format=%B|%an|%aI", second)
		Expect(original).To(HavePrefix("other\n\nwhy it matters\n|Fixture|"))
		Expect(gitFixture(fixture.Path, "log", "-1", "--format=%B|%an|%aI", tip)).To(Equal(original))
		Expect(gitFixture(fixture.Path, "log", "-1", "--format=%cn", tip)).To(Equal(fixtureSignature.Name))
		Expect(gitFixture(fixture.Path, "rev-parse", tip+"~2")).To(Equal(fixture.Main))
		Expect(gitFixture(fixture.Path, "ls-tree", "--name-only", "-r", tip)).To(Equal("README.md\nfeature.txt\nother.txt"))
		Expect(worktrees()).To(Equal(before))
	})

	It("reports the conflicted files of a commit that does not apply", func(ctx SpecContext) {
		conflict := fixture.commitOn(fixtureCommit{Branch: "conflict", From: fixture.First, Path: "README.md", Content: "conflict\n", Message: "conflicting readme"})
		_, err := git.CherryPickLocal(ctx, pick(conflict))
		var conflictErr *git.CherryPickConflictError
		Expect(errors.As(err, &conflictErr)).To(BeTrue(), "%v", err)
		Expect(*conflictErr).To(Equal(git.CherryPickConflictError{Commit: conflict, Files: []string{"README.md"}}))
	})

	It("refuses a commit whose changes are already on the base", func(ctx SpecContext) {
		_, err := git.CherryPickLocal(ctx, pick(fixture.Main))
		var empty *git.CherryPickEmptyError
		Expect(errors.As(err, &empty)).To(BeTrue(), "%v", err)
		Expect(*empty).To(Equal(git.CherryPickEmptyError{Commit: fixture.Main, Base: fixture.Main}))
	})

	It("takes a root commit's changes against the empty tree", func(ctx SpecContext) {
		orphan := fixture.commitOn(fixtureCommit{Branch: "orphan", From: fixture.First, Path: "orphan.txt", Content: "o\n", Message: "orphan"})
		gitFixture(fixture.work(), "checkout", "-q", "--orphan", "root")
		gitFixture(fixture.work(), "rm", "-rqf", ".")
		writeFixtureFile(fixture.work(), "root.txt", "r\n")
		gitFixture(fixture.work(), "add", "-A")
		gitFixture(fixture.work(), "commit", "-q", "-m", "root")
		gitFixture(fixture.work(), "push", "-q", fixture.Path, "root")
		root := gitFixture(fixture.work(), "rev-parse", "HEAD")
		Expect(gitFixture(fixture.Path, "rev-list", "--parents", "--max-count=1", root)).To(Equal(root))

		tip, err := git.CherryPickLocal(ctx, git.LocalCherryPickOptions{Dir: fixture.Path, Base: orphan, CherryPick: []string{root}})
		Expect(err).ToNot(HaveOccurred())
		Expect(gitFixture(fixture.Path, "ls-tree", "--name-only", "-r", tip)).To(Equal("README.md\norphan.txt\nroot.txt"))

		_, err = git.CherryPickLocal(ctx, git.LocalCherryPickOptions{Dir: fixture.Path, Base: fixture.First, CherryPick: []string{fixture.First}})
		var empty *git.CherryPickEmptyError
		Expect(errors.As(err, &empty)).To(BeTrue(), "%v", err)
	})

	It("replays a merge commit only against the mainline it is given", func(ctx SpecContext) {
		fixture.commitOn(fixtureCommit{Branch: "side", From: fixture.Main, Path: "side.txt", Content: "side\n", Message: "side"})
		merge := fixture.mergeInto("feature", "side")
		_, err := git.CherryPickLocal(ctx, pick(merge))
		Expect(err).To(MatchError(And(ContainSubstring(merge), ContainSubstring("mainline"))))

		options := pick(merge)
		options.Mainline = 1
		tip, err := git.CherryPickLocal(ctx, options)
		Expect(err).ToNot(HaveOccurred())
		Expect(gitFixture(fixture.Path, "ls-tree", "--name-only", "-r", tip)).To(Equal("README.md\nside.txt"))
	})

	It("rejects an unresolvable base before replaying anything", func(ctx SpecContext) {
		options := pick(fixture.Feature)
		options.Base = "missing/main"
		_, err := git.CherryPickLocal(ctx, options)
		Expect(err).To(MatchError(ContainSubstring("missing/main")))
	})
})

var _ = Describe("PushLocal", func() {
	It("pushes a commit to a new branch of the named remote", func(ctx SpecContext) {
		fixture := newBareRepo("rules")
		target := filepath.Join(fixtureTempDir(), "target.git")
		Expect(os.MkdirAll(target, 0o755)).To(Succeed())
		gitFixture(target, "init", "-q", "--bare")
		gitFixture(fixture.Path, "remote", "add", "country", target)

		Expect(git.PushLocal(ctx, git.LocalPushOptions{Dir: fixture.Path, Remote: "country", Commit: fixture.Feature, Branch: "pr/topic"})).To(Succeed())
		Expect(gitFixture(target, "rev-parse", "refs/heads/pr/topic")).To(Equal(fixture.Feature))

		err := git.PushLocal(ctx, git.LocalPushOptions{Dir: fixture.Path, Remote: "country", Commit: "abc", Branch: "pr/topic"})
		Expect(err).To(MatchError(ContainSubstring("full 40-character")))
	})
})
