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
	It("rejects paths escaping the repository", func(ctx SpecContext) {
		_, err := git.CheckVisibility(ctx, git.VisibilityOptions{Directory: fixtureTempDir(), Paths: []git.VisibilityPath{{Path: "../outside"}}})
		Expect(err).To(HaveOccurred())
	})
})
