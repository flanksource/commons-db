package git_test

import (
	"io/fs"
	"os"
	"path/filepath"

	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("local repository read in place", func() {
	const branch = "Add/topic"
	var (
		fixture bareRepo
		cache   *git.Cache
		repo    *git.Repository
		topic   string
	)
	BeforeEach(func() {
		fixture = newBareRepo("local-read")
		work := fixture.work()
		gitFixture(work, "checkout", "-q", "-b", branch, fixture.Main)
		writeFixtureFile(work, "rules/rule.xml", "<Rule/>\n")
		gitFixture(work, "add", "-A")
		gitFixture(work, "commit", "-q", "-m", "topic")
		topic = gitFixture(work, "rev-parse", "HEAD")
		gitFixture(work, "checkout", "-q", "--detach", fixture.First)
		gitFixture(work, "worktree", "add", "-q", "--detach", filepath.Join(fixture.Root, "linked"), fixture.Feature)
		cache = &git.Cache{Root: GinkgoT().TempDir()}
		repo = newRepository("file://"+work, git.Auth{})
	})
	counter := func(timings *rpchttp.Timings, metric, name string) int64 {
		value, _ := timings.Counter(metric, name)
		return value
	}

	It("resolves a branch of a working repository with a detached HEAD and linked worktrees natively", func(ctx SpecContext) {
		timed, timings := rpchttp.WithTimings(ctx)
		ref, err := repo.Resolve(timed, branch)
		Expect(err).NotTo(HaveOccurred())
		head, err := repo.Resolve(timed, "HEAD")
		Expect(err).NotTo(HaveOccurred())
		Expect([]any{ref, head.SHA, head.Kind, counter(timings, "git", "ls_remote")}).To(Equal([]any{
			git.Ref{Name: branch, FullName: "refs/heads/" + branch, SHA: topic, Kind: git.RefBranch}, fixture.First, git.RefHead, int64(0)}))
	})

	It("reads trees, blobs, history and changes without fetching or creating a mirror", func(ctx SpecContext) {
		timed, timings := rpchttp.WithTimings(ctx)
		tree, err := cache.OpenTree(timed, git.TreeOptions{Repository: repo, Revision: branch})
		Expect(err).NotTo(HaveOccurred())
		body, err := fs.ReadFile(tree, "rules/rule.xml")
		Expect(err).NotTo(HaveOccurred())
		Expect(tree.Close()).To(Succeed())
		blobs, err := cache.ReadBlobs(timed, repo, []git.BlobRequest{{SHA: topic, Path: "README.md"}})
		Expect(err).NotTo(HaveOccurred())
		history, err := cache.History(timed, repo, branch, git.HistoryOptions{Detail: git.HistoryNames})
		Expect(err).NotTo(HaveOccurred())
		changes, err := cache.Changes(timed, git.ChangesOptions{Repository: repo, Base: fixture.Main, Head: branch})
		Expect(err).NotTo(HaveOccurred())
		mergeBase, err := cache.MergeBase(timed, repo, git.MergeBaseOptions{Base: "feature", Head: branch})
		Expect(err).NotTo(HaveOccurred())
		files, err := cache.Files(timed, repo, topic)
		Expect(err).NotTo(HaveOccurred())
		_, mirrorErr := os.Stat(filepath.Join(cache.Root, repo.URL.CacheKey(), "mirror.git"))
		Expect([]any{string(body), string(blobs[0].Content), len(history), len(changes), changes[0].Path, mergeBase, files,
			counter(timings, "git_fetch", "ops"), counter(timings, "git", "ls_remote"), os.IsNotExist(mirrorErr)}).To(Equal([]any{
			"<Rule/>\n", "second\n", 3, 1, "rules/rule.xml", fixture.Main, []string{"README.md", "rules/rule.xml"},
			int64(0), int64(0), true}))
	})

	It("fails loudly for a commit the local repository lacks", func(ctx SpecContext) {
		_, err := cache.ReadBlobs(ctx, repo, []git.BlobRequest{{SHA: "0123456789012345678901234567890123456789", Path: "README.md"}})
		Expect(err).To(MatchError(git.ErrRevisionNotFound))
	})

	It("names a local repository that moved away instead of failing on its borrowed objects, and reads it again once back", func(ctx SpecContext) {
		read := func(path string) error {
			_, err := cache.ReadBlobs(ctx, repo, []git.BlobRequest{{SHA: topic, Path: path}})
			return err
		}
		Expect(read("README.md")).To(Succeed())
		work, moved := fixture.work(), fixture.work()+"-moved"
		Expect(os.Rename(work, moved)).To(Succeed())
		err := read("rules/rule.xml")
		Expect(err).To(MatchError(git.ErrNotRepository))
		Expect(err).To(MatchError(ContainSubstring(work)))
		Expect(os.Rename(moved, work)).To(Succeed())
		Expect(read("rules/rule.xml")).To(Succeed())
	})
})
