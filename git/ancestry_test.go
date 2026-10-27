package git_test

import (
	"path/filepath"

	rpchttp "github.com/flanksource/clicky/rpc/http"
	"github.com/flanksource/commons-db/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("local ancestry", func() {
	gitOps := func(timings *rpchttp.Timings) int64 {
		ops, _ := timings.Counter("git", "ops")
		return ops
	}

	It("ranks a local head's ancestors nearest first in one spawn, bounded by Max", func(ctx SpecContext) {
		fixture := newBareRepo("local-ancestry")
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		timed, timings := rpchttp.WithTimings(ctx)
		ancestors, err := cache.Ancestors(timed, git.AncestorsOptions{Directory: fixture.work(), Head: fixture.Feature, Max: 1024})
		Expect(err).NotTo(HaveOccurred())
		Expect(ancestors).To(Equal([]string{fixture.Feature, fixture.Main, fixture.First}))
		Expect(gitOps(timings)).To(Equal(int64(1)))
		nearest, err := cache.Ancestors(ctx, git.AncestorsOptions{Directory: fixture.work(), Head: "feature", Max: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(nearest).To(Equal([]string{fixture.Feature, fixture.Main}))
	})

	It("ranks a cached repository's ancestors from its mirror", func(ctx SpecContext) {
		fixture := newBareRepo("mirror-ancestry")
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		ancestors, err := cache.Ancestors(ctx, git.AncestorsOptions{Repository: newRepository(fixture.FileURL(), git.Auth{}), Head: fixture.Feature, Max: 1024})
		Expect(err).NotTo(HaveOccurred())
		Expect(ancestors).To(Equal([]string{fixture.Feature, fixture.Main, fixture.First}))
	})

	It("refuses ancestry without a bound or naming both or neither source", func(ctx SpecContext) {
		fixture := newBareRepo("invalid-ancestry")
		cache := &git.Cache{Root: GinkgoT().TempDir()}
		_, err := cache.Ancestors(ctx, git.AncestorsOptions{Directory: fixture.work(), Head: "main"})
		Expect(err).To(MatchError(git.ErrInvalidSource))
		_, err = cache.Ancestors(ctx, git.AncestorsOptions{Head: "main", Max: 1})
		Expect(err).To(MatchError(git.ErrInvalidSource))
		_, err = cache.Ancestors(ctx, git.AncestorsOptions{Directory: fixture.work(), Head: "missing", Max: 1})
		Expect(err).To(MatchError(git.ErrRevisionNotFound))
	})

	It("resolves a working directory's root, prefix and commit, and reads committed entries there", func(ctx SpecContext) {
		fixture := newBareRepo("local-revision")
		fixture.commitOn(fixtureCommit{Branch: "main", Path: "rules/pc/data.xml", Content: "<root/>", Message: "Add rules"})
		main := gitFixture(fixture.work(), "rev-parse", "main")
		timed, timings := rpchttp.WithTimings(ctx)
		revision, err := git.ResolveLocalRevision(timed, git.LocalRevisionOptions{Directory: filepath.Join(fixture.work(), "rules"), Revision: "main"})
		Expect(err).NotTo(HaveOccurred())
		Expect(revision).To(Equal(git.LocalRevision{Root: fixture.work(), Prefix: "rules", Commit: main}))
		Expect(gitOps(timings)).To(Equal(int64(2)))
		writeFixtureFile(fixture.work(), "rules/pc/data.xml", "<dirty/>")
		files, err := git.ReadLocalFiles(ctx, git.LocalFilesOptions{Root: revision.Root, Commit: revision.Commit, Files: []string{"rules/pc/data.xml", "missing.yaml"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(files).To(Equal(map[string]git.LocalFile{"rules/pc/data.xml": {Mode: "100644", Type: "blob", Content: []byte("<root/>")}}))
		directories, err := git.ReadLocalFiles(ctx, git.LocalFilesOptions{Root: revision.Root, Commit: revision.Commit, Files: []string{"rules"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(directories).To(Equal(map[string]git.LocalFile{"rules": {Mode: "040000", Type: "tree"}}))
	})

	It("resolves a bare repository as its own root", func(ctx SpecContext) {
		fixture := newBareRepo("bare-revision")
		revision, err := git.ResolveLocalRevision(ctx, git.LocalRevisionOptions{Directory: fixture.Path, Revision: "v1"})
		Expect(err).NotTo(HaveOccurred())
		Expect(revision).To(Equal(git.LocalRevision{Root: fixture.Path, Bare: true, Commit: fixture.First}))
	})

	It("fails loudly outside a repository and for an unknown revision", func(ctx SpecContext) {
		_, err := git.ResolveLocalRevision(ctx, git.LocalRevisionOptions{Directory: fixtureTempDir(), Revision: "main"})
		Expect(err).To(MatchError(git.ErrNotRepository))
		fixture := newBareRepo("unknown-revision")
		_, err = git.ResolveLocalRevision(ctx, git.LocalRevisionOptions{Directory: fixture.work(), Revision: "missing"})
		Expect(err).To(MatchError(git.ErrRevisionNotFound))
	})
})
