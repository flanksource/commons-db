package git_test

import (
	"os"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Cache.History and Cache.Show", func() {
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

	It("reads a pinned cached commit after the remote becomes unavailable", func(ctx SpecContext) {
		repo = mirroredRepository(fixture)
		_, err := cache.History(ctx, repo, fixture.Main, git.HistoryOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Rename(fixture.Path, fixture.Path+".offline")).To(Succeed())
		commits, err := cache.History(ctx, repo, fixture.Main, git.HistoryOptions{Limit: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(commits).To(HaveLen(1))
		Expect(commits[0].SHA).To(Equal(fixture.Main))
	})

	It("returns commit metadata without file details or rename work when requested", func(ctx SpecContext) {
		log := argvRecorder()
		commits, err := cache.History(ctx, repo, fixture.Main, git.HistoryOptions{Limit: 1, Detail: git.HistoryMetadata})
		Expect(err).NotTo(HaveOccurred())
		Expect(commits).To(HaveLen(1))
		Expect(commits[0].SHA).To(Equal(fixture.Main))
		Expect(commits[0].Files).To(BeEmpty())
		Expect(commits[0].Status).To(BeEmpty())
		commands, err := os.ReadFile(log)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(commands)).NotTo(ContainSubstring("--find-renames"))
		Expect(string(commands)).NotTo(ContainSubstring("--name-status"))
	})

	changes := func(commits []git.Commit) []git.Commit {
		out := make([]git.Commit, len(commits))
		for i, commit := range commits {
			out[i] = git.Commit{SHA: commit.SHA, Files: commit.Files, Status: commit.Status, Renames: commit.Renames}
		}
		return out
	}

	It("lists the commits of one ref touching Paths newest first, keeping the newest Limit", func(ctx SpecContext) {
		created := fixture.commitOn(fixtureCommit{Branch: "main", Path: "a.txt", Content: "a1\n", Message: "add a"})
		fixture.commitOn(fixtureCommit{Branch: "main", Path: "b.txt", Content: "b\n", Message: "add b"})
		changed := fixture.commitOn(fixtureCommit{Branch: "main", Path: "a.txt", Content: "a2\n", Message: "change a"})

		commits, err := cache.History(ctx, repo, "main", git.HistoryOptions{Paths: []string{"a.txt"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(changes(commits)).To(Equal([]git.Commit{
			{SHA: changed, Files: []string{"a.txt"}, Status: map[string]git.FileStatus{"a.txt": git.FileModified}},
			{SHA: created, Files: []string{"a.txt"}, Status: map[string]git.FileStatus{"a.txt": git.FileAdded}},
		}))

		limited, err := cache.History(ctx, repo, "main", git.HistoryOptions{Paths: []string{"a.txt"}, Limit: 1})
		Expect(err).ToNot(HaveOccurred())
		Expect(changes(limited)).To(Equal(changes(commits[:1])))
	})

	It("follows a renamed file back past the rename only with Follow", func(ctx SpecContext) {
		created := fixture.commitOn(fixtureCommit{Branch: "main", Path: "a.txt", Content: "line one\nline two\nline three\n", Message: "add a"})
		renamed := fixture.changeOn("main", "rename a", "mv", "a.txt", "c.txt")
		edited := fixture.commitOn(fixtureCommit{Branch: "main", Path: "c.txt", Content: "line one\nline two\nline three\nline four\n", Message: "edit c"})
		renamedStatus := map[string]git.FileStatus{"c.txt": git.FileRenamed}

		followed, err := cache.History(ctx, repo, "main", git.HistoryOptions{Paths: []string{"c.txt"}, Follow: true})
		Expect(err).ToNot(HaveOccurred())
		Expect(changes(followed)).To(Equal([]git.Commit{
			{SHA: edited, Files: []string{"c.txt"}, Status: map[string]git.FileStatus{"c.txt": git.FileModified}},
			{SHA: renamed, Files: []string{"c.txt"}, Status: renamedStatus, Renames: []git.Rename{{From: "a.txt", To: "c.txt"}}},
			{SHA: created, Files: []string{"a.txt"}, Status: map[string]git.FileStatus{"a.txt": git.FileAdded}},
		}))

		unfollowed, err := cache.History(ctx, repo, "main", git.HistoryOptions{Paths: []string{"c.txt"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(unfollowed).To(HaveLen(2))
		Expect(unfollowed[1].Status).To(Equal(map[string]git.FileStatus{"c.txt": git.FileAdded}))
	})

	It("matches Paths literally, so a file under a glob-like directory does not match its siblings", func(ctx SpecContext) {
		const globbed = "[Old] Plan/data.xml"
		fixture.commitOn(fixtureCommit{Branch: "main", Path: "d Plan/data.xml", Content: "sibling\n", Message: "add a sibling the glob would match"})
		literal := fixture.commitOn(fixtureCommit{Branch: "main", Path: globbed, Content: "plan\n", Message: "add the bracketed plan"})
		want := []git.Commit{{SHA: literal, Files: []string{globbed}, Status: map[string]git.FileStatus{globbed: git.FileAdded}}}

		commits, err := cache.History(ctx, repo, "main", git.HistoryOptions{Paths: []string{globbed}})
		Expect(err).ToNot(HaveOccurred())
		Expect(changes(commits)).To(Equal(want))

		followed, err := cache.History(ctx, repo, "main", git.HistoryOptions{Paths: []string{globbed}, Follow: true})
		Expect(err).ToNot(HaveOccurred())
		Expect(changes(followed)).To(Equal(want))
	})

	It("refuses Follow without exactly one path", func(ctx SpecContext) {
		_, err := cache.History(ctx, repo, "main", git.HistoryOptions{Paths: []string{"a.txt", "b.txt"}, Follow: true})
		Expect(err).To(MatchError(ContainSubstring("exactly one path, got 2")))
	})

	It("shows a file's bytes at an old commit and ErrPathNotFound once it is deleted", func(ctx SpecContext) {
		const oldContent = "old\n"
		old := fixture.commitOn(fixtureCommit{Branch: "main", Path: "a.txt", Content: oldContent, Message: "add a"})
		fixture.commitOn(fixtureCommit{Branch: "main", Path: "a.txt", Content: "new\n", Message: "change a"})
		deleted := fixture.changeOn("main", "remove a", "rm", "-q", "a.txt")

		content, err := cache.Show(ctx, repo, old, "a.txt")
		Expect(err).ToNot(HaveOccurred())
		Expect(string(content)).To(Equal(oldContent))

		_, err = cache.Show(ctx, repo, deleted, "a.txt")
		Expect(err).To(MatchError(git.ErrPathNotFound))
	})

	It("refuses a short SHA", func(ctx SpecContext) {
		_, err := cache.Show(ctx, repo, fixture.Main[:12], "README.md")
		Expect(err).To(MatchError(ContainSubstring("full 40-character")))
	})
})
