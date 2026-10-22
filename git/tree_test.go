package git_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing/fstest"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("native Git tree filesystem", func() {
	It("reads selected files without recursively listing unrelated paths", func(ctx SpecContext) {
		fixture := newBareRepo("selected-tree")
		fixture.commitOn(fixtureCommit{Branch: "main", Path: "rules/rule.xml", Content: "<Rule/>", Message: "Add rule"})
		head := fixture.commitOn(fixtureCommit{Branch: "main", Path: "unrelated/deep/file.txt", Content: "Unrelated", Message: "Add unrelated path"})
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		trace := argvRecorder()
		tree, err := cache.OpenTree(ctx, git.TreeOptions{Directory: fixture.Path, Revision: head})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(tree.Close()).To(Succeed()) })
		body, err := fs.ReadFile(tree, "rules/rule.xml")
		Expect(err).NotTo(HaveOccurred())
		Expect(string(body)).To(Equal("<Rule/>"))
		commands, err := os.ReadFile(trace)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(commands)).NotTo(ContainSubstring("ls-tree -r"))
		entries, err := fs.ReadDir(tree, "unrelated")
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].Name()).To(Equal("deep"))
	})

	It("reports the selected commit without inspecting a checkout or loading its history", func(ctx SpecContext) {
		fixture := newBareRepo("tree-provenance")
		sha := fixture.commitOn(fixtureCommit{Branch: "main", Path: "rule.xml", Content: "<Old/>", Message: "Original rule"})
		fixture.commitOn(fixtureCommit{Branch: "main", Path: "rule.xml", Content: "<New/>", Message: "Later rule"})
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		recorder := argvRecorder()
		tree, err := cache.OpenTree(ctx, git.TreeOptions{Directory: fixture.Path, Revision: sha})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(tree.Close()).To(Succeed()) })
		metadata, err := tree.Metadata(git.InspectOptions{RepositoryHint: fixture.FileURL(), BranchHint: "main"})
		Expect(err).NotTo(HaveOccurred())
		Expect(metadata.SHA).To(Equal(sha))
		Expect(metadata.Branch).To(Equal("main"))
		Expect(metadata.LastCommit.Subject).To(Equal("Original rule"))
		Expect(metadata.Dirty.Clean()).To(BeTrue())
		commands, err := os.ReadFile(recorder)
		Expect(err).NotTo(HaveOccurred())
		for _, command := range []string{"status", "worktree", "--unshallow", "ls-remote"} {
			Expect(string(commands)).NotTo(ContainSubstring(command))
		}
	})

	It("reads a bare commit through one object process without materializing files", func(ctx SpecContext) {
		fixture := newBareRepo("tree")
		const name = "[Plan]\trule\n.xml"
		const body = "<Rule>\x00\xff</Rule>\n"
		fixture.commitOn(fixtureCommit{Branch: "main", Path: "rules/" + name, Content: body, Message: "Add rule"})
		head := fixture.commitOn(fixtureCommit{Branch: "main", Path: "rules/empty.xml", Message: "Add empty rule"})
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		log := argvRecorder()
		tree, err := cache.OpenTree(ctx, git.TreeOptions{Directory: fixture.Path, Revision: head})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(tree.Close()).To(Succeed()) })
		Expect(tree.Info().SHA).To(Equal(head))
		content, err := fs.ReadFile(tree, "rules/"+name)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(content)).To(Equal(body))
		empty, err := fs.ReadFile(tree, "rules/empty.xml")
		Expect(err).NotTo(HaveOccurred())
		Expect(empty).To(BeEmpty())
		_, err = fs.ReadFile(tree, "rules/absent.xml")
		Expect(err).To(MatchError(fs.ErrNotExist))
		entries, err := fs.ReadDir(tree, "rules")
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(2))
		Expect(entries[0].Name()).To(Equal(name))
		var paths []string
		Expect(fs.WalkDir(tree, ".", func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			paths = append(paths, path)
			return nil
		})).To(Succeed())
		Expect(paths).To(Equal([]string{".", "README.md", "rules", "rules/" + name, "rules/empty.xml"}))
		Expect(fstest.TestFS(tree, "README.md", "rules/"+name, "rules/empty.xml")).To(Succeed())
		Expect(tree.Close()).To(Succeed())
		commands, err := os.ReadFile(log)
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.Count(string(commands), "cat-file --batch")).To(Equal(1))
		Expect(string(commands)).NotTo(ContainSubstring("worktree"))
		Expect(string(commands)).NotTo(ContainSubstring("archive"))
		allocations, err := os.ReadDir(cache.Root)
		Expect(err).NotTo(HaveOccurred())
		Expect(allocations).To(BeEmpty())
		_, err = fs.ReadFile(tree, "README.md")
		Expect(err).To(MatchError(fs.ErrClosed))
	})

	It("keeps a working repository's selected commit after its branch and files change", func(ctx SpecContext) {
		fixture := newBareRepo("pinned-tree")
		head := fixture.commitOn(fixtureCommit{Branch: "main", Path: "rule.xml", Content: "<Old/>", Message: "Add rule"})
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		tree, err := cache.OpenTree(ctx, git.TreeOptions{Directory: fixture.work(), Revision: "main"})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(tree.Close()).To(Succeed()) })
		fixture.commitOn(fixtureCommit{Branch: "main", Path: "rule.xml", Content: "<New/>", Message: "Replace rule"})
		writeFixtureFile(fixture.work(), "rule.xml", "<Dirty/>")
		body, err := fs.ReadFile(tree, "rule.xml")
		Expect(err).NotTo(HaveOccurred())
		Expect([]string{tree.Info().SHA, string(body)}).To(Equal([]string{head, "<Old/>"}))
		_, err = os.Stat(filepath.Join(fixture.work(), "rule.xml"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("opens a pinned cached repository without rediscovering an unavailable remote", func(ctx SpecContext) {
		fixture := newBareRepo("cached-tree")
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		repo := newRepository(fixture.FileURL(), git.Auth{})
		_, err := cache.ReadBlobs(ctx, repo, []git.BlobRequest{{SHA: fixture.Main, Path: "README.md"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.Rename(fixture.Path, fixture.Path+"-offline")).To(Succeed())
		tree, err := cache.OpenTree(ctx, git.TreeOptions{Repository: repo, Revision: fixture.Main})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(tree.Close()).To(Succeed()) })
		body, err := fs.ReadFile(tree, "README.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(body).NotTo(BeEmpty())
	})

	It("reports request cancellation while releasing its owned process and metadata", func(ctx SpecContext) {
		fixture := newBareRepo("cancelled-tree")
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		request, cancel := context.WithCancel(ctx)
		defer cancel()
		tree, err := cache.OpenTree(request, git.TreeOptions{Directory: fixture.Path, Revision: fixture.Main})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(tree.Close()).To(Succeed()) })
		_, err = fs.ReadFile(tree, "README.md")
		Expect(err).NotTo(HaveOccurred())
		cancel()
		_, err = fs.ReadFile(tree, "README.md")
		Expect(err).To(MatchError(context.Canceled))
		Expect(tree.Close()).To(Succeed())
		entries, err := os.ReadDir(cache.Root)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(BeEmpty())
	})
})
