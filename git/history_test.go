package git_test

import (
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

	changes := func(commits []git.Commit) []git.Commit {
		out := make([]git.Commit, len(commits))
		for i, commit := range commits {
			out[i] = git.Commit{SHA: commit.SHA, Files: commit.Files, Status: commit.Status, Renames: commit.Renames}
		}
		return out
	}

	It("lists the commits of one ref touching Paths newest first, keeping the newest Limit", func(ctx SpecContext) {
		created := fixture.commitOn("main", "", "a.txt", "a1\n", "add a")
		fixture.commitOn("main", "", "b.txt", "b\n", "add b")
		changed := fixture.commitOn("main", "", "a.txt", "a2\n", "change a")

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
		created := fixture.commitOn("main", "", "a.txt", "line one\nline two\nline three\n", "add a")
		renamed := fixture.changeOn("main", "rename a", "mv", "a.txt", "c.txt")
		edited := fixture.commitOn("main", "", "c.txt", "line one\nline two\nline three\nline four\n", "edit c")
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
		fixture.commitOn("main", "", "d Plan/data.xml", "sibling\n", "add a sibling the glob would match")
		literal := fixture.commitOn("main", "", globbed, "plan\n", "add the bracketed plan")
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
		old := fixture.commitOn("main", "", "a.txt", oldContent, "add a")
		fixture.commitOn("main", "", "a.txt", "new\n", "change a")
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
