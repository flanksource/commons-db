package git_test

import (
	"os"
	"path/filepath"
	"strings"

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
		repo := newRepository(fixture.FileURL(), git.Auth{})
		worktree, err := cache.Checkout(ctx, repo, git.CheckoutOptions{Ref: head, Depth: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(worktree.Release()).To(Succeed())
		Expect(os.Rename(fixture.Path, fixture.Path+"-offline")).To(Succeed())
		changes, err := cache.Changes(ctx, repo, git.ChangesOptions{Base: base, Head: head})
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
		changes, err := cache.Changes(ctx, repo, git.ChangesOptions{Base: base, Head: head})
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
})
