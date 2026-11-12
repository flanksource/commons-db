package git_test

import (
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"os"
	"path/filepath"
)

var _ = Describe("Working file blame", func() {
	It("keeps unborn and untracked source independent of committed provenance", func(ctx SpecContext) {
		directory := fixtureTempDir()
		gitFixture(directory, "init", "-q")
		options := git.WorkingBlameOptions{Directory: directory, Path: "guide.md", Contents: []byte("new prose\n")}
		result, err := git.BlameWorkingFile(ctx, options)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Unavailable).To(Equal("Repository has no commits"))
		Expect(os.WriteFile(filepath.Join(directory, "tracked.md"), []byte("original\n"), 0o644)).To(Succeed())
		gitFixture(directory, "add", "tracked.md")
		gitFixture(directory, "-c", "user.name=Example", "-c", "user.email=example@example.com", "commit", "-qm", "docs: add guide")
		result, err = git.BlameWorkingFile(ctx, options)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Unavailable).To(Equal("File is not present in HEAD"))
		options.Path = "tracked.md"
		options.Contents = []byte("original\nnew prose\n")
		result, err = git.BlameWorkingFile(ctx, options)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Lines).To(HaveLen(2))
		Expect(result.Lines[0].Commit).NotTo(BeNil())
		Expect(result.Lines[1].Commit).To(BeNil())
	})
})
