package git_test

import (
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Git-visible paths", func() {
	It("excludes ignored files and directories while retaining tracked matches", func(ctx SpecContext) {
		directory := fixtureTempDir()
		Expect(os.MkdirAll(filepath.Join(directory, "ignored"), 0o755)).To(Succeed())
		Expect(os.MkdirAll(filepath.Join(directory, "internal"), 0o755)).To(Succeed())
		for path, content := range map[string]string{
			".gitignore":           "ignored/\n*.gen.go\n",
			"main.go":              "package app\n",
			"private.go":           "package app\n",
			"tracked.gen.go":       "package app\n",
			"ignored/hidden.go":    "package ignored\n",
			"internal/.gitignore":  "!keep.gen.go\n",
			"internal/keep.gen.go": "package internal\n",
		} {
			Expect(os.WriteFile(filepath.Join(directory, path), []byte(content), 0o644)).To(Succeed())
		}
		gitFixture(directory, "init", "-q")
		Expect(os.WriteFile(filepath.Join(directory, ".git", "info", "exclude"), []byte("private.go\n"), 0o644)).To(Succeed())
		gitFixture(directory, "add", "-f", "tracked.gen.go")

		visible, err := git.ListVisiblePaths(ctx, directory)
		Expect(err).ToNot(HaveOccurred())
		for _, path := range []string{"main.go", "tracked.gen.go", "internal", "internal/keep.gen.go"} {
			Expect(visible.Contains(filepath.Join(directory, path))).To(BeTrue(), path)
		}
		for _, path := range []string{"ignored", "ignored/hidden.go", "private.go"} {
			Expect(visible.Contains(filepath.Join(directory, path))).To(BeFalse(), path)
		}
		ignored, err := visible.IsIgnored(ctx, filepath.Join(directory, "ignored"))
		Expect(err).ToNot(HaveOccurred())
		Expect(ignored).To(BeTrue())
	})

	It("treats directories outside Git as visible", func(ctx SpecContext) {
		directory := fixtureTempDir()
		visible, err := git.ListVisiblePaths(ctx, directory)
		Expect(err).ToNot(HaveOccurred())
		Expect(visible.Contains(filepath.Join(directory, "main.go"))).To(BeTrue())
		ignored, err := visible.IsIgnored(ctx, filepath.Join(directory, "nested"))
		Expect(err).ToNot(HaveOccurred())
		Expect(ignored).To(BeFalse())
	})
})
