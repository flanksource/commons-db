package git_test

import (
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Immediate Git visibility", func() {
	It("retains native nonmatching records when every supplied path is visible", func(ctx SpecContext) {
		directory := fixtureTempDir()
		gitFixture(directory, "init", "-q")
		result, err := git.CheckVisibility(ctx, git.VisibilityOptions{Directory: directory, Paths: []git.VisibilityPath{{Path: "docs", Directory: true}, {Path: "guide.md"}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal([]git.PathVisibility{{Path: "docs", Visible: true}, {Path: "guide.md", Visible: true}}))
	})
	It("applies ordered overrides after native ignore rules without enumerating descendants", func(ctx SpecContext) {
		directory := fixtureTempDir()
		gitFixture(directory, "init", "-q")
		Expect(os.WriteFile(filepath.Join(directory, ".gitignore"), []byte("private/\n*.draft.md\n"), 0o644)).To(Succeed())
		paths := []git.VisibilityPath{{Path: "private", Directory: true}, {Path: "guide.md"}, {Path: "keep.draft.md"}, {Path: "other.draft.md"}}
		result, err := git.CheckVisibility(ctx, git.VisibilityOptions{Directory: directory, Paths: paths, Overrides: []string{"guide.md", "!keep.draft.md"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal([]git.PathVisibility{
			{Path: "private", Visible: false, Rule: "private/"},
			{Path: "guide.md", Visible: false, Rule: "guide.md"},
			{Path: "keep.draft.md", Visible: true, Rule: "!keep.draft.md"},
			{Path: "other.draft.md", Visible: false, Rule: "*.draft.md"},
		}))
	})
	It("reports a tracked path beyond a symlinked directory as visible instead of failing", func(ctx SpecContext) {
		directory := fixtureTempDir()
		gitFixture(directory, "init", "-q")
		Expect(os.WriteFile(filepath.Join(directory, ".gitignore"), []byte("*.log\n"), 0o644)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(directory, "fixtures", "plans"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(directory, "fixtures", "plans", "smoke.yaml"), []byte("kind: TestPlan\n"), 0o644)).To(Succeed())
		gitFixture(directory, "add", ".")
		gitFixture(directory, "commit", "-q", "-m", "fixtures")
		Expect(os.RemoveAll(filepath.Join(directory, "fixtures"))).To(Succeed())
		Expect(os.Symlink(fixtureTempDir(), filepath.Join(directory, "fixtures"))).To(Succeed())

		paths := []git.VisibilityPath{{Path: "fixtures/plans/smoke.yaml"}, {Path: "debug.log"}, {Path: "fixtures"}}
		result, err := git.CheckVisibility(ctx, git.VisibilityOptions{Directory: directory, Paths: paths})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal([]git.PathVisibility{
			{Path: "fixtures/plans/smoke.yaml", Visible: true, BeyondSymlink: "fixtures"},
			{Path: "debug.log", Visible: false, Rule: "*.log"},
			{Path: "fixtures", Visible: true},
		}))
	})
	It("matches tracked files against ignore rules only when the index is ignored", func(ctx SpecContext) {
		directory := fixtureTempDir()
		gitFixture(directory, "init", "-q")
		Expect(os.MkdirAll(filepath.Join(directory, "dist"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(directory, "dist", "bundle.js"), []byte("bundle\n"), 0o644)).To(Succeed())
		gitFixture(directory, "add", "-f", "dist/bundle.js")
		gitFixture(directory, "commit", "-q", "-m", "bundle")
		Expect(os.WriteFile(filepath.Join(directory, ".gitignore"), []byte("dist/\n"), 0o644)).To(Succeed())

		paths := []git.VisibilityPath{{Path: "dist/bundle.js"}}
		tracked, err := git.CheckVisibility(ctx, git.VisibilityOptions{Directory: directory, Paths: paths})
		Expect(err).NotTo(HaveOccurred())
		Expect(tracked).To(Equal([]git.PathVisibility{{Path: "dist/bundle.js", Visible: true}}))
		patterns, err := git.CheckVisibility(ctx, git.VisibilityOptions{Directory: directory, Paths: paths, IgnoreIndex: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(patterns).To(Equal([]git.PathVisibility{{Path: "dist/bundle.js", Visible: false, Rule: "dist/"}}))
	})
	It("treats every path as visible outside a repository", func(ctx SpecContext) {
		directory := fixtureTempDir()
		Expect(os.WriteFile(filepath.Join(directory, ".gitignore"), []byte("*.log\n"), 0o644)).To(Succeed())
		result, err := git.CheckVisibility(ctx, git.VisibilityOptions{Directory: directory, Paths: []git.VisibilityPath{{Path: "debug.log"}}, IgnoreIndex: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal([]git.PathVisibility{{Path: "debug.log", Visible: true}}))
	})
	It("rejects paths escaping the repository", func(ctx SpecContext) {
		_, err := git.CheckVisibility(ctx, git.VisibilityOptions{Directory: fixtureTempDir(), Paths: []git.VisibilityPath{{Path: "../outside"}}})
		Expect(err).To(HaveOccurred())
	})
})
