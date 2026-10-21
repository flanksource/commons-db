package git_test

import (
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Prepared sources", func() {
	var fixture bareRepo
	var cache *git.Cache
	BeforeEach(func() {
		fixture = newBareRepo("source")
		cache = &git.Cache{Root: filepath.Join(GinkgoT().TempDir(), ".tmp", "sources")}
	})

	It("prepares exact historical bytes and returns defensive metadata and changes", func(ctx SpecContext) {
		writeFixtureFile(fixture.work(), "README.md", "unstaged\n")
		before := gitFixture(fixture.work(), "status", "--porcelain")
		source, err := cache.PrepareSource(ctx, git.SourceOptions{Directory: fixture.work(), Kind: git.SourceCommit, Revision: fixture.Main})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(source.Release)
		info := source.Info()
		Expect(info.RepositoryRoot).To(Equal(fixture.work()))
		Expect(info.BaseCommit).To(Equal(fixture.First))
		Expect(info.TargetCommit).To(Equal(fixture.Main))
		Expect(info.TreeID).To(Equal(gitFixture(fixture.work(), "rev-parse", fixture.Main+"^{tree}")))
		Expect(os.ReadFile(filepath.Join(info.Dir, "README.md"))).To(BeEquivalentTo("second\n"))
		Expect(source.Changes()).To(Equal([]git.SourceChange{{Path: "README.md", Status: git.FileModified}}))
		changes := source.Changes()
		changes[0].Path = "mutated"
		Expect(source.Changes()[0].Path).To(Equal("README.md"))
		Expect(gitFixture(fixture.work(), "status", "--porcelain")).To(Equal(before))
		Expect(source.Release()).To(Succeed())
		Expect(source.Release()).To(Succeed())
		Expect(info.Dir).NotTo(BeADirectory())
	})

	It("captures partial staging without modifying the index and omits intent-to-add entries", func(ctx SpecContext) {
		writeFixtureFile(fixture.work(), "README.md", "staged\n")
		gitFixture(fixture.work(), "add", "README.md")
		writeFixtureFile(fixture.work(), "intent.txt", "intent only\n")
		gitFixture(fixture.work(), "add", "-N", "intent.txt")
		writeFixtureFile(fixture.work(), "README.md", "unstaged\n")
		indexPath := filepath.Join(fixture.work(), ".git", "index")
		before, err := os.ReadFile(indexPath)
		Expect(err).NotTo(HaveOccurred())
		source, err := cache.PrepareSource(ctx, git.SourceOptions{Directory: fixture.work(), Kind: git.SourceStaged})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(source.Release)
		Expect(source.Info().TargetCommit).To(BeEmpty())
		Expect(source.Info().BaseCommit).To(Equal(fixture.Feature))
		Expect(source.Info().TreeID).NotTo(BeEmpty())
		Expect(os.ReadFile(filepath.Join(source.Info().Dir, "README.md"))).To(BeEquivalentTo("staged\n"))
		Expect(filepath.Join(source.Info().Dir, "intent.txt")).NotTo(BeAnExistingFile())
		Expect(source.Changes()).To(Equal([]git.SourceChange{{Path: "README.md", Status: git.FileModified}}))
		after, err := os.ReadFile(indexPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(after).To(Equal(before))
		writeFixtureFile(fixture.work(), "README.md", "later index\n")
		gitFixture(fixture.work(), "add", "README.md")
		Expect(os.ReadFile(filepath.Join(source.Info().Dir, "README.md"))).To(BeEquivalentTo("staged\n"))
	})

	It("returns live net changes and nonignored untracked paths with exact filenames", func(ctx SpecContext) {
		writeFixtureFile(fixture.work(), ".gitignore", "ignored.txt\n")
		gitFixture(fixture.work(), "add", ".gitignore")
		gitFixture(fixture.work(), "commit", "-qm", "ignore")
		writeFixtureFile(fixture.work(), "README.md", "temporary\n")
		gitFixture(fixture.work(), "add", "README.md")
		writeFixtureFile(fixture.work(), "README.md", "second\n")
		writeFixtureFile(fixture.work(), "new [file]\n.txt", "new\n")
		writeFixtureFile(fixture.work(), "ignored.txt", "ignored\n")
		source, err := cache.PrepareSource(ctx, git.SourceOptions{Directory: fixture.work(), Kind: git.SourceWorktree})
		Expect(err).NotTo(HaveOccurred())
		Expect(source.Info().Dir).To(Equal(fixture.work()))
		Expect(source.Info().TreeID).To(BeEmpty())
		Expect(source.Changes()).To(Equal([]git.SourceChange{{Path: "new [file]\n.txt", Status: git.FileAdded}}))
		Expect(source.Release()).To(Succeed())
		Expect(fixture.work()).To(BeADirectory())
	})

	DescribeTable("resolves commit and range baselines", func(ctx SpecContext, revision string, base bool, expected []git.SourceChange) {
		source, err := cache.PrepareSource(ctx, git.SourceOptions{Directory: fixture.work(), Kind: git.SourceCommit, Revision: revision})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(source.Release)
		if base {
			Expect(source.Info().BaseCommit).To(Equal(fixture.First))
		} else {
			Expect(source.Info().BaseCommit).To(BeEmpty())
		}
		Expect(source.Changes()).To(Equal(expected))
	},
		Entry("root commit", "v1", false, []git.SourceChange{{Path: "README.md", Status: git.FileAdded}}),
		Entry("two dots", "v1..main", true, []git.SourceChange{{Path: "README.md", Status: git.FileModified}}),
		Entry("three dots", "v1...feature", true, []git.SourceChange{{Path: "README.md", Status: git.FileModified}, {Path: "feature.txt", Status: git.FileAdded}}),
	)

	It("supports an initial index without HEAD", func(ctx SpecContext) {
		root := GinkgoT().TempDir()
		gitFixture(root, "init", "-q", "-b", "main")
		writeFixtureFile(root, "first.txt", "first\n")
		gitFixture(root, "add", "first.txt")
		source, err := cache.PrepareSource(ctx, git.SourceOptions{Directory: root, Kind: git.SourceStaged})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(source.Release)
		Expect(source.Info().BaseCommit).To(BeEmpty())
		Expect(source.Changes()).To(Equal([]git.SourceChange{{Path: "first.txt", Status: git.FileAdded}}))
	})

	It("omits staged additions deleted from an unborn working tree", func(ctx SpecContext) {
		root := GinkgoT().TempDir()
		gitFixture(root, "init", "-q", "-b", "main")
		writeFixtureFile(root, "removed.txt", "removed\n")
		gitFixture(root, "add", "removed.txt")
		Expect(os.Remove(filepath.Join(root, "removed.txt"))).To(Succeed())
		source, err := cache.PrepareSource(ctx, git.SourceOptions{Directory: root, Kind: git.SourceWorktree})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(source.Release)
		Expect(source.Changes()).To(Equal([]git.SourceChange{}))
	})

	It("keeps simultaneous preparations independent", func(ctx SpecContext) {
		options := git.SourceOptions{Directory: fixture.work(), Kind: git.SourceCommit, Revision: "main"}
		first, err := cache.PrepareSource(ctx, options)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(first.Release)
		second, err := cache.PrepareSource(ctx, options)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(second.Release)
		Expect(first.Info().Dir).NotTo(Equal(second.Info().Dir))
		Expect(first.Info().TreeID).To(Equal(second.Info().TreeID))
		Expect(first.Release()).To(Succeed())
		Expect(second.Info().Dir).To(BeADirectory())
	})

	It("writes no objects or refs into the original repository", func(ctx SpecContext) {
		writeFixtureFile(fixture.work(), "new.txt", "new staged tree\n")
		gitFixture(fixture.work(), "add", "new.txt")
		objects := gitFixture(fixture.work(), "count-objects", "-v")
		refs := gitFixture(fixture.work(), "show-ref")
		source, err := cache.PrepareSource(ctx, git.SourceOptions{Directory: fixture.work(), Kind: git.SourceStaged})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(source.Release)
		Expect(gitFixture(fixture.work(), "count-objects", "-v")).To(Equal(objects))
		Expect(gitFixture(fixture.work(), "show-ref")).To(Equal(refs))
		blob := gitFixture(fixture.work(), "rev-parse", ":new.txt")
		Expect(os.Remove(filepath.Join(fixture.work(), ".git", "objects", blob[:2], blob[2:]))).To(Succeed())
		Expect(os.ReadFile(filepath.Join(source.Info().Dir, "new.txt"))).To(BeEquivalentTo("new staged tree\n"))
	})

	It("rejects invalid selectors and revisions with typed errors", func(ctx SpecContext) {
		_, err := cache.PrepareSource(ctx, git.SourceOptions{Directory: fixture.work()})
		Expect(err).To(MatchError(git.ErrInvalidSource))
		_, err = cache.PrepareSource(ctx, git.SourceOptions{Directory: fixture.work(), Kind: git.SourceStaged, Revision: "HEAD"})
		Expect(err).To(MatchError(git.ErrInvalidSource))
		_, err = cache.PrepareSource(ctx, git.SourceOptions{Directory: fixture.work(), Kind: git.SourceCommit, Revision: "missing"})
		Expect(err).To(MatchError(git.ErrRevisionNotFound))
	})
})
