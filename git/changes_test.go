package git_test

import (
	"os"
	"path/filepath"
	"strings"

	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Cache.Changes", func() {
	It("compares cached endpoints without fetching history from a shallow mirror", func(ctx SpecContext) {
		fixture := newBareRepo("shallow-changes")
		base := fixture.commitOn(fixtureCommit{Branch: "main", Path: "rule.xml", Content: "<Old/>", Message: "Add rule"})
		head := fixture.commitOn(fixtureCommit{Branch: "main", Path: "rule.xml", Content: "<New/>", Message: "Change rule"})
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		repo := mirroredRepository(fixture)
		worktree, err := cache.Checkout(ctx, repo, git.CheckoutOptions{Ref: head, Depth: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(worktree.Release()).To(Succeed())
		Expect(os.Rename(fixture.Path, fixture.Path+"-offline")).To(Succeed())
		changes, err := cache.Changes(ctx, git.ChangesOptions{Repository: repo, Base: base, Head: head})
		Expect(err).NotTo(HaveOccurred())
		Expect(changes).To(HaveLen(1))
		Expect(changes[0].Path).To(Equal("rule.xml"))
	})

	It("reads native object changes including mode-only changes without loading blobs", func(ctx SpecContext) {
		fixture := newBareRepo("changes")
		base := fixture.commitOn(fixtureCommit{Branch: "main", Path: "removed.xml", Content: "<Removed/>\n", Message: "Add removed rule"})
		fixture.commitOn(fixtureCommit{Branch: "main", Path: "[Plan]\trule\n.xml", Content: "<Rule/>\n", Message: "Add unusual path"})
		fixture.changeOn("main", "Remove rule", "rm", "removed.xml")
		writeFixtureFile(fixture.work(), "empty.xml", "")
		gitFixture(fixture.work(), "add", "empty.xml")
		gitFixture(fixture.work(), "update-index", "--chmod=+x", "README.md")
		gitFixture(fixture.work(), "commit", "-q", "-m", "Add empty rule and change mode")
		gitFixture(fixture.work(), "push", "-q", fixture.Path, "main")
		head := gitFixture(fixture.work(), "rev-parse", "HEAD")
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		repo := newRepository(fixture.FileURL(), git.Auth{})
		log := argvRecorder()
		changes, err := cache.Changes(ctx, git.ChangesOptions{Repository: repo, Base: base, Head: head})
		Expect(err).NotTo(HaveOccurred())
		Expect(changes).To(HaveLen(4))
		byPath := map[string]git.Change{}
		for _, change := range changes {
			byPath[change.Path] = change
		}
		Expect(byPath["removed.xml"].Status).To(Equal(git.FileDeleted))
		Expect(byPath["empty.xml"].Status).To(Equal(git.FileAdded))
		Expect(byPath["[Plan]\trule\n.xml"].Status).To(Equal(git.FileAdded))
		mode := byPath["README.md"]
		Expect([]string{mode.OldMode, mode.NewMode, mode.OldObject}).To(Equal([]string{"100644", "100755", mode.NewObject}))
		Expect(mode.NewObject).NotTo(BeEmpty())
		commands, err := os.ReadFile(log)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(commands)).NotTo(ContainSubstring("cat-file blob"))
		Expect(string(commands)).NotTo(ContainSubstring("worktree"))
		Expect(strings.Count(string(commands), "diff-tree")).To(Equal(1))
		_, err = os.Stat(filepath.Join(cache.Root, repo.URL.CacheKey(), "worktrees"))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})

	It("limits changes to literal paths", func(ctx SpecContext) {
		fixture := newBareRepo("path-changes")
		base := fixture.commitOn(fixtureCommit{Branch: "main", Path: "rules/[a].xml", Content: "<Old/>", Message: "Add rule"})
		fixture.commitOn(fixtureCommit{Branch: "main", Path: "rules/[a].xml", Content: "<New/>", Message: "Change rule"})
		writeFixtureFile(fixture.work(), "rules/a.xml", "<Glob/>")
		head := fixture.commitOn(fixtureCommit{Branch: "main", Path: "other/b.xml", Content: "<Other/>", Message: "Add unrelated"})
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		changes, err := cache.Changes(ctx, git.ChangesOptions{Repository: newRepository(fixture.FileURL(), git.Auth{}),
			Base: base, Head: head, Paths: []string{"rules/[a].xml"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(paths(changes)).To(Equal([]string{"rules/[a].xml"}))
	})

	It("reports a rename once with both paths and blob objects", func(ctx SpecContext) {
		fixture := newBareRepo("renamed-changes")
		base := fixture.commitOn(fixtureCommit{Branch: "main", Path: "old/rule.xml", Content: "<Rule>\n  <Body/>\n</Rule>\n", Message: "Add rule"})
		head := fixture.changeOn("main", "Move rule", "mv", "old/rule.xml", "new.xml")
		blob := gitFixture(fixture.Path, "rev-parse", head+":new.xml")
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		changes, err := cache.Changes(ctx, git.ChangesOptions{Repository: newRepository(fixture.FileURL(), git.Auth{}),
			Base: base, Head: head, Renames: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(changes).To(Equal([]git.Change{{Path: "new.xml", OldPath: "old/rule.xml", Status: git.FileRenamed,
			OldObject: blob, NewObject: blob, OldMode: "100644", NewMode: "100644"}}))
	})

	It("compares a local working repository without fetching it into a mirror", func(ctx SpecContext) {
		fixture := newBareRepo("local-changes")
		base := fixture.commitOn(fixtureCommit{Branch: "main", Path: "rule.xml", Content: "<Old/>", Message: "Add rule"})
		head := fixture.commitOn(fixtureCommit{Branch: "main", Path: "rule.xml", Content: "<New/>", Message: "Change rule"})
		writeFixtureFile(fixture.work(), "rule.xml", "<Dirty/>")
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		timed, timings := rpchttp.WithTimings(ctx)
		changes, err := cache.Changes(timed, git.ChangesOptions{Directory: fixture.work(), Base: base, Head: "main"})
		Expect(err).NotTo(HaveOccurred())
		Expect(paths(changes)).To(Equal([]string{"rule.xml"}))
		Expect(changes[0].NewObject).To(Equal(gitFixture(fixture.work(), "rev-parse", head+":rule.xml")))
		fetches, _ := timings.Counter("git_fetch", "ops")
		Expect(fetches).To(BeZero())
		allocations, err := os.ReadDir(cache.Root)
		Expect(err).NotTo(HaveOccurred())
		Expect(allocations).To(BeEmpty())
	})

	It("rejects options naming both or neither of a repository and a directory", func(ctx SpecContext) {
		fixture := newBareRepo("ambiguous-changes")
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		_, err := cache.Changes(ctx, git.ChangesOptions{Repository: newRepository(fixture.FileURL(), git.Auth{}), Directory: fixture.Path,
			Base: fixture.First, Head: fixture.Main})
		Expect(err).To(MatchError(git.ErrInvalidSource))
		_, err = cache.Changes(ctx, git.ChangesOptions{Base: fixture.First, Head: fixture.Main})
		Expect(err).To(MatchError(git.ErrInvalidSource))
	})
})

func paths(changes []git.Change) []string {
	result := make([]string, len(changes))
	for index, change := range changes {
		result[index] = change.Path
	}
	return result
}
