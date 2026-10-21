package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Prepared sources edge cases", func() {
	var fixture bareRepo
	var cache *git.Cache
	BeforeEach(func() {
		fixture = newBareRepo("source-edge")
		cache = &git.Cache{Root: filepath.Join(GinkgoT().TempDir(), ".tmp", "sources")}
	})

	prepare := func(ctx context.Context, kind git.SourceKind, revision string) *git.PreparedSource {
		GinkgoHelper()
		source, err := cache.PrepareSource(ctx, git.SourceOptions{Directory: fixture.work(), Kind: kind, Revision: revision})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(source.Release)
		return source
	}

	It("supports split indexes without changing their files", func(ctx SpecContext) {
		writeFixtureFile(fixture.work(), "README.md", "staged split\n")
		gitFixture(fixture.work(), "add", "README.md")
		gitFixture(fixture.work(), "update-index", "--split-index")
		index, err := os.ReadFile(filepath.Join(fixture.work(), ".git", "index"))
		Expect(err).NotTo(HaveOccurred())
		sharedPath := gitFixture(fixture.work(), "rev-parse", "--shared-index-path")
		if !filepath.IsAbs(sharedPath) {
			sharedPath = filepath.Join(fixture.work(), sharedPath)
		}
		shared, err := os.ReadFile(sharedPath)
		Expect(err).NotTo(HaveOccurred())
		source := prepare(ctx, git.SourceStaged, "")
		Expect(os.ReadFile(filepath.Join(source.Info().Dir, "README.md"))).To(BeEquivalentTo("staged split\n"))
		Expect(os.ReadFile(filepath.Join(fixture.work(), ".git", "index"))).To(Equal(index))
		Expect(os.ReadFile(sharedPath)).To(Equal(shared))
	})

	It("materializes the full tree from a sparse index", func(ctx SpecContext) {
		writeFixtureFile(fixture.work(), "docs/readme.txt", "docs\n")
		writeFixtureFile(fixture.work(), "hidden/deep/file.txt", "hidden\n")
		gitFixture(fixture.work(), "add", "docs/readme.txt", "hidden/deep/file.txt")
		gitFixture(fixture.work(), "commit", "-qm", "directories")
		gitFixture(fixture.work(), "sparse-checkout", "init", "--cone", "--sparse-index")
		gitFixture(fixture.work(), "sparse-checkout", "set", "docs")
		writeFixtureFile(fixture.work(), "docs/readme.txt", "staged docs\n")
		gitFixture(fixture.work(), "add", "docs/readme.txt")
		source := prepare(ctx, git.SourceStaged, "")
		Expect(os.ReadFile(filepath.Join(source.Info().Dir, "hidden/deep/file.txt"))).To(BeEquivalentTo("hidden\n"))
		Expect(source.Changes()).To(Equal([]git.SourceChange{{Path: "docs/readme.txt", Status: git.FileModified}}))
	})

	It("uses the index of the selected linked worktree", func(ctx SpecContext) {
		linked := filepath.Join(fixture.Root, "linked")
		gitFixture(fixture.work(), "worktree", "add", "--detach", linked, fixture.Main)
		writeFixtureFile(linked, "README.md", "linked staged\n")
		gitFixture(linked, "add", "README.md")
		writeFixtureFile(fixture.work(), "README.md", "other checkout\n")
		source, err := cache.PrepareSource(ctx, git.SourceOptions{Directory: linked, Kind: git.SourceStaged})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(source.Release)
		Expect(source.Info().RepositoryRoot).To(Equal(linked))
		Expect(source.Info().BaseCommit).To(Equal(fixture.Main))
		Expect(os.ReadFile(filepath.Join(source.Info().Dir, "README.md"))).To(BeEquivalentTo("linked staged\n"))
	})

	It("retains deletions and treats renamed paths literally", func(ctx SpecContext) {
		name := "renamed\t[file]\n.txt"
		gitFixture(fixture.work(), "mv", "feature.txt", name)
		gitFixture(fixture.work(), "rm", "README.md")
		gitFixture(fixture.work(), "commit", "-qm", "rename and delete")
		source := prepare(ctx, git.SourceCommit, "HEAD")
		Expect(source.Changes()).To(Equal([]git.SourceChange{
			{Path: "README.md", Status: git.FileDeleted},
			{Path: "feature.txt", Status: git.FileDeleted},
			{Path: name, Status: git.FileAdded},
		}))
		Expect(os.ReadFile(filepath.Join(source.Info().Dir, name))).To(BeEquivalentTo("feature\n"))
	})

	It("compares merge commits with their first parent and three-dot ranges with the merge base", func(ctx SpecContext) {
		gitFixture(fixture.work(), "checkout", "main")
		writeFixtureFile(fixture.work(), "left.txt", "left\n")
		gitFixture(fixture.work(), "add", "left.txt")
		gitFixture(fixture.work(), "commit", "-qm", "left")
		gitFixture(fixture.work(), "checkout", "feature")
		writeFixtureFile(fixture.work(), "right.txt", "right\n")
		gitFixture(fixture.work(), "add", "right.txt")
		gitFixture(fixture.work(), "commit", "-qm", "right")
		parent := gitFixture(fixture.work(), "rev-parse", "HEAD")
		gitFixture(fixture.work(), "merge", "--no-ff", "-m", "merge", "main")
		source := prepare(ctx, git.SourceCommit, "HEAD")
		Expect(source.Info().BaseCommit).To(Equal(parent))
		Expect(source.Changes()).To(Equal([]git.SourceChange{{Path: "left.txt", Status: git.FileAdded}}))
		rangeSource := prepare(ctx, git.SourceCommit, "main...HEAD")
		Expect(rangeSource.Changes()).To(Equal([]git.SourceChange{{Path: "feature.txt", Status: git.FileAdded}, {Path: "right.txt", Status: git.FileAdded}}))
	})

	It("keeps an empty selection explicit and handles omitted endpoints", func(ctx SpecContext) {
		source := prepare(ctx, git.SourceCommit, "HEAD..")
		Expect(source.Changes()).To(BeEmpty())
		Expect(source.Changes()).NotTo(BeNil())
		Expect(source.Info().BaseCommit).To(Equal(fixture.Feature))
		Expect(source.Info().TargetCommit).To(Equal(fixture.Feature))
	})

	It("preserves executable modes, safe symlinks and raw bytes despite checkout attributes", func(ctx SpecContext) {
		writeFixtureFile(fixture.work(), ".gitattributes", "*.txt text eol=crlf\n")
		writeFixtureFile(fixture.work(), "payload.txt", "stored\nbytes\n")
		writeFixtureFile(fixture.work(), "run.sh", "#!/bin/sh\nexit 0\n")
		Expect(os.Chmod(filepath.Join(fixture.work(), "run.sh"), 0o755)).To(Succeed())
		Expect(os.Symlink("payload.txt", filepath.Join(fixture.work(), "link"))).To(Succeed())
		gitFixture(fixture.work(), "add", ".gitattributes", "payload.txt", "run.sh", "link")
		gitFixture(fixture.work(), "commit", "-qm", "attributes and modes")
		source := prepare(ctx, git.SourceCommit, "HEAD")
		Expect(os.ReadFile(filepath.Join(source.Info().Dir, "payload.txt"))).To(BeEquivalentTo("stored\nbytes\n"))
		Expect(os.Readlink(filepath.Join(source.Info().Dir, "link"))).To(Equal("payload.txt"))
		info, err := os.Stat(filepath.Join(source.Info().Dir, "run.sh"))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o755)))
	})

	It("supports the repository's native SHA-256 object format", func(ctx SpecContext) {
		root := GinkgoT().TempDir()
		gitFixture(root, "init", "-q", "--object-format=sha256", "-b", "main")
		writeFixtureFile(root, "file.txt", "sha256\n")
		gitFixture(root, "add", "file.txt")
		gitFixture(root, "commit", "-qm", "initial")
		source, err := cache.PrepareSource(ctx, git.SourceOptions{Directory: root, Kind: git.SourceCommit, Revision: "HEAD"})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(source.Release)
		Expect(source.Info().TargetCommit).To(HaveLen(64))
		Expect(source.Info().TreeID).To(HaveLen(64))
	})

	It("resolves case-distinct packed refs without conflating their targets", func(ctx SpecContext) {
		gitFixture(fixture.work(), "branch", "CaseRef", fixture.First)
		gitFixture(fixture.work(), "pack-refs", "--all", "--prune")
		gitFixture(fixture.work(), "branch", "caseref", fixture.Main)
		gitFixture(fixture.work(), "pack-refs", "--all", "--prune")
		first := prepare(ctx, git.SourceCommit, "refs/heads/CaseRef")
		second := prepare(ctx, git.SourceCommit, "refs/heads/caseref")
		Expect(first.Info().TargetCommit).To(Equal(fixture.First))
		Expect(second.Info().TargetCommit).To(Equal(fixture.Main))
	})

	It("rejects conflicts and cleans incomplete preparation", func(ctx SpecContext) {
		blob := gitFixture(fixture.work(), "rev-parse", "HEAD:README.md")
		command := exec.CommandContext(ctx, "git", "update-index", "--index-info")
		command.Dir = fixture.work()
		command.Stdin = strings.NewReader("0 " + strings.Repeat("0", len(blob)) + "\tREADME.md\n100644 " + blob + " 1\tREADME.md\n")
		Expect(command.Run()).To(Succeed())
		_, err := cache.PrepareSource(ctx, git.SourceOptions{Directory: fixture.work(), Kind: git.SourceStaged})
		Expect(errors.Is(err, git.ErrUnmergedIndex)).To(BeTrue(), err)
		entries, err := os.ReadDir(cache.Root)
		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(BeEmpty())
	})

	It("rejects gitlinks and escaping symlinks", func(ctx SpecContext) {
		Expect(os.Symlink("../escape", filepath.Join(fixture.work(), "escape"))).To(Succeed())
		gitFixture(fixture.work(), "add", "escape")
		_, err := cache.PrepareSource(ctx, git.SourceOptions{Directory: fixture.work(), Kind: git.SourceStaged})
		Expect(errors.Is(err, git.ErrUnsupportedSource)).To(BeTrue(), err)
		gitFixture(fixture.work(), "reset", "--", "escape")
		gitFixture(fixture.work(), "update-index", "--add", "--cacheinfo", "160000,"+fixture.Main+",submodule")
		_, err = cache.PrepareSource(ctx, git.SourceOptions{Directory: fixture.work(), Kind: git.SourceStaged})
		Expect(errors.Is(err, git.ErrUnsupportedSource)).To(BeTrue(), err)
	})

	It("rejects symlink chains escaping through a missing target", func(ctx SpecContext) {
		Expect(os.Mkdir(filepath.Join(fixture.work(), "directory"), 0o755)).To(Succeed())
		Expect(os.Symlink("..", filepath.Join(fixture.work(), "directory", "back"))).To(Succeed())
		Expect(os.Symlink("directory/back/../missing", filepath.Join(fixture.work(), "indirect"))).To(Succeed())
		gitFixture(fixture.work(), "add", "directory/back", "indirect")
		_, err := cache.PrepareSource(ctx, git.SourceOptions{Directory: fixture.work(), Kind: git.SourceStaged})
		Expect(errors.Is(err, git.ErrUnsupportedSource)).To(BeTrue(), err)
	})

	It("fails on missing local objects without contacting a configured promisor remote", func(ctx SpecContext) {
		remote := newHTTPRemote(fixture.Root, false)
		gitFixture(fixture.work(), "config", "remote.origin.url", remote.URL(fixture))
		gitFixture(fixture.work(), "config", "remote.origin.promisor", "true")
		gitFixture(fixture.work(), "config", "extensions.partialClone", "origin")
		blob := gitFixture(fixture.work(), "rev-parse", "HEAD:feature.txt")
		Expect(os.Remove(filepath.Join(fixture.work(), ".git", "objects", blob[:2], blob[2:]))).To(Succeed())
		_, err := cache.PrepareSource(ctx, git.SourceOptions{Directory: fixture.work(), Kind: git.SourceCommit, Revision: "HEAD"})
		Expect(err).To(HaveOccurred())
		writeFixtureFile(fixture.work(), "feature.txt", "changed\n")
		_, err = cache.PrepareSource(ctx, git.SourceOptions{Directory: fixture.work(), Kind: git.SourceWorktree})
		Expect(err).To(HaveOccurred())
		Expect(remote.Requests()).To(BeEmpty())
	})

	It("returns typed errors for unrelated histories, nonrepositories and bare input", func(ctx SpecContext) {
		gitFixture(fixture.work(), "checkout", "--orphan", "unrelated")
		gitFixture(fixture.work(), "commit", "-qm", "unrelated")
		_, err := cache.PrepareSource(ctx, git.SourceOptions{Directory: fixture.work(), Kind: git.SourceCommit, Revision: "main...unrelated"})
		Expect(errors.Is(err, git.ErrUnrelatedHistories)).To(BeTrue(), err)
		_, err = cache.PrepareSource(ctx, git.SourceOptions{Directory: GinkgoT().TempDir(), Kind: git.SourceWorktree})
		Expect(errors.Is(err, git.ErrNotRepository)).To(BeTrue(), err)
		_, err = cache.PrepareSource(ctx, git.SourceOptions{Directory: fixture.Path, Kind: git.SourceCommit, Revision: "main"})
		Expect(errors.Is(err, git.ErrUnsupportedSource)).To(BeTrue(), err)
	})

	It("rejects a canceled request without leaving allocations", func(ctx SpecContext) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		_, err := cache.PrepareSource(canceled, git.SourceOptions{Directory: fixture.work(), Kind: git.SourceCommit, Revision: "HEAD"})
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		entries, readErr := os.ReadDir(cache.Root)
		if !errors.Is(readErr, os.ErrNotExist) {
			Expect(readErr).NotTo(HaveOccurred())
			Expect(entries).To(BeEmpty())
		}
	})
})
