package git_test

import (
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Index files", func() {
	It("includes tracked ignored files and optional untracked source while excluding ignored scratch", func(ctx SpecContext) {
		root := GinkgoT().TempDir()
		gitFixture(root, "init", "--quiet")
		for name, body := range map[string]string{".gitignore": "ignored-*\n", "tracked.go": "tracked", "ignored-tracked.go": "tracked", "untracked.go": "new", "ignored-scratch.go": "scratch"} {
			Expect(os.WriteFile(filepath.Join(root, name), []byte(body), 0o600)).To(Succeed())
		}
		gitFixture(root, "add", "-f", ".gitignore", "tracked.go", "ignored-tracked.go")
		files, err := git.IndexFiles(ctx, git.IndexFilesOptions{Directory: root})
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(ConsistOf(".gitignore", "tracked.go", "ignored-tracked.go"))
		files, err = git.IndexFiles(ctx, git.IndexFilesOptions{Directory: root, IncludeUntracked: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(ConsistOf(".gitignore", "tracked.go", "ignored-tracked.go", "untracked.go"))
	})
})
