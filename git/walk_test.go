package git_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Git-aware walk", func() {
	write := func(root, name, body string) {
		Expect(os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(root, name), []byte(body), 0o644)).To(Succeed())
	}
	walk := func(ctx context.Context, root string) ([]string, error) {
		var paths []string
		err := git.WalkDir(ctx, root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				relative, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				paths = append(paths, filepath.ToSlash(relative))
			}
			return nil
		})
		return paths, err
	}
	It("honors native and global excludes while retaining tracked matches and nested repositories", func(ctx SpecContext) {
		root := fixtureTempDir()
		gitFixture(root, "init", "-q")
		global := fixtureTempDir()
		write(global, "config", "[core]\n excludesFile = "+filepath.Join(global, "ignore")+"\n")
		write(global, "ignore", "global/\n")
		GinkgoT().Setenv("GIT_CONFIG_GLOBAL", filepath.Join(global, "config"))
		write(root, ".gitignore", "hidden/\n*.draft\n")
		write(root, ".git/info/exclude", "private/\n")
		for _, name := range []string{"hidden/data.xml", "private/data.xml", "global/data.xml", "tracked.draft", "keep/data.xml", "nested/keep.draft", "nested/drop.draft"} {
			write(root, name, "body")
		}
		write(root, "nested/.gitignore", "!keep.draft\n")
		gitFixture(root, "add", "-f", "tracked.draft")
		nested := filepath.Join(root, "repo")
		write(nested, "data.xml", "body")
		write(nested, "ignored/data.xml", "body")
		write(nested, ".gitignore", "ignored/\n")
		gitFixture(nested, "init", "-q")
		paths, err := walk(ctx, root)
		Expect(err).NotTo(HaveOccurred())
		Expect(paths).To(ConsistOf(".gitignore", "tracked.draft", "keep/data.xml", "nested/.gitignore", "nested/keep.draft", "repo/.gitignore", "repo/data.xml"))
	})
	It("preserves repository excludesFile precedence and immediate visibility", func(ctx SpecContext) {
		root := fixtureTempDir()
		gitFixture(root, "init", "-q")
		global := fixtureTempDir()
		write(global, "config", "[core]\n excludesFile = "+filepath.Join(global, "ignore")+"\n")
		write(global, "ignore", "global.xml\n")
		GinkgoT().Setenv("GIT_CONFIG_GLOBAL", filepath.Join(global, "config"))
		write(root, "global.xml", "body")
		visibility, err := git.CheckVisibility(ctx, git.VisibilityOptions{Directory: root, Paths: []git.VisibilityPath{{Path: "global.xml"}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(visibility[0].Visible).To(BeFalse())
		gitFixture(root, "config", "core.excludesFile", "")
		paths, err := walk(ctx, root)
		Expect(err).NotTo(HaveOccurred())
		Expect(paths).To(ConsistOf("global.xml"))
	})
	It("does not apply ignores outside repositories", func(ctx SpecContext) {
		root := fixtureTempDir()
		write(root, ".gitignore", "hidden/\n")
		write(root, "hidden/data.xml", "body")
		paths, err := walk(ctx, root)
		Expect(err).NotTo(HaveOccurred())
		Expect(paths).To(ConsistOf(".gitignore", "hidden/data.xml"))
	})
	It("uses a linked worktree index and skips its metadata file", func(ctx SpecContext) {
		root := fixtureTempDir()
		gitFixture(root, "init", "-q")
		write(root, "data.xml", "body")
		write(root, ".gitignore", "hidden/\n")
		gitFixture(root, "add", "data.xml", ".gitignore")
		gitFixture(root, "commit", "-qm", "fixture")
		worktree := filepath.Join(fixtureTempDir(), "worktree")
		gitFixture(root, "worktree", "add", "--detach", worktree)
		write(worktree, "hidden/data.xml", "body")
		paths, err := walk(ctx, worktree)
		Expect(err).NotTo(HaveOccurred())
		Expect(paths).To(ConsistOf(".gitignore", "data.xml"))
	})
	It("honors callback pruning and failures", func(ctx SpecContext) {
		root := fixtureTempDir()
		write(root, "skip/data.xml", "body")
		var visited []string
		err := git.WalkDir(ctx, root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Name() == "skip" {
				return filepath.SkipDir
			}
			visited = append(visited, path)
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(visited).To(Equal([]string{root}))
		err = git.WalkDir(ctx, root, func(string, fs.DirEntry, error) error { return fs.ErrPermission })
		Expect(err).To(MatchError(fs.ErrPermission))
	})
	It("returns cancellation and filesystem errors", func(ctx SpecContext) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err := walk(canceled, fixtureTempDir())
		Expect(err).To(MatchError(context.Canceled))
		_, err = walk(ctx, filepath.Join(fixtureTempDir(), "missing"))
		Expect(os.IsNotExist(err)).To(BeTrue())
	})
})
