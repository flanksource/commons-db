package workspace

import (
	"os"
	"path/filepath"

	dbcontext "github.com/flanksource/commons-db/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gstruct"
)

var _ = Describe("Prepare", func() {
	var ctx dbcontext.Context
	var baseDir string
	BeforeEach(func() {
		ctx = dbcontext.New()
		baseDir = GinkgoT().TempDir()
	})
	prepare := func(opts Options) *Workspace {
		ws, err := Prepare(ctx, opts)
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		DeferCleanup(ws.Release)
		return ws
	}

	It("rejects an unknown kind", func() {
		_, err := Prepare(ctx, Options{Source: initRepo(), Kind: "bogus"})
		Expect(err).To(MatchError(ContainSubstring(`unknown workspace kind "bogus"`)))
	})

	Describe("head", func() {
		It("uses the source checkout in place, from any directory inside it", func() {
			source := dirtyRepo()
			ws := prepare(Options{Source: filepath.Join(source, "vendor"), Kind: KindHead})
			Expect(ws).To(gstruct.PointTo(gstruct.MatchFields(gstruct.IgnoreExtras, gstruct.Fields{
				"Kind": Equal(KindHead), "Source": Equal(source), "Dir": Equal(source),
				"Base": Equal(git(source, "rev-parse", "HEAD")), "Branch": Equal("main"),
			})))
		})
		It("refuses worktree-only options rather than ignoring them", func() {
			_, err := Prepare(ctx, Options{Source: initRepo(), Kind: KindHead, Ignored: true})
			Expect(err).To(MatchError(ContainSubstring("head workspace")))
		})
	})

	Describe("clean", func() {
		It("adds a worktree at the base with none of the source's work", func() {
			source := dirtyRepo()
			ws := prepare(Options{Source: source, Kind: KindClean, BaseDir: baseDir})
			Expect(ws.Dir).NotTo(Equal(source))
			Expect(filepath.Join(ws.Dir, "staged.txt")).NotTo(BeAnExistingFile())
			Expect(filepath.Join(ws.Dir, "untracked.txt")).NotTo(BeAnExistingFile())
			Expect(filepath.Join(ws.Dir, "ignored.txt")).NotTo(BeAnExistingFile())
			Expect(read(ws.Dir, "seed.txt")).To(Equal("seed\n"))
			Expect(git(ws.Dir, "status", "--porcelain")).To(BeEmpty())
			Expect(ws.Branch).To(HavePrefix("shell/"))
			Expect(ws.Carried).To(BeEmpty())
		})
		It("clones ignored content and records it, leaving tracked paths alone", func() {
			source := dirtyRepo()
			ws := prepare(Options{Source: source, Kind: KindClean, BaseDir: baseDir, Ignored: true})
			Expect(read(ws.Dir, "ignored.txt")).To(Equal("ignored\n"))
			Expect(read(ws.Dir, "vendor/nested/dep.txt")).To(Equal("dep\n"))
			Expect(ws.Ignored).To(ConsistOf("ignored.txt", "vendor/"))
			Expect(filepath.Join(ws.Dir, ".git", "refs")).NotTo(BeADirectory())
		})
		It("clones ignored files independently of the source", func() {
			source := dirtyRepo()
			ws := prepare(Options{Source: source, Kind: KindClean, BaseDir: baseDir, Ignored: true})
			write(ws.Dir, "ignored.txt", "rewritten\n")
			Expect(read(source, "ignored.txt")).To(Equal("ignored\n"))
		})
		It("checks out an existing branch as-is", func() {
			source := initRepo()
			git(source, "checkout", "-q", "-b", "shell/previous")
			write(source, "previous.txt", "previous\n")
			git(source, "add", "previous.txt")
			git(source, "commit", "-q", "-m", "previous")
			tip := git(source, "rev-parse", "HEAD")
			git(source, "checkout", "-q", "main")

			ws := prepare(Options{Source: source, Kind: KindClean, BaseDir: baseDir, Branch: "shell/previous", Existing: true})
			Expect(ws.Base).To(Equal(tip))
			Expect(git(ws.Dir, "symbolic-ref", "--short", "HEAD")).To(Equal("shell/previous"))
		})
		It("refuses a revision expression as an existing branch", func() {
			source := initRepo()
			for _, branch := range []string{"main~1", "HEAD"} {
				_, err := Prepare(ctx, Options{Source: source, Kind: KindClean, BaseDir: baseDir, Branch: branch, Existing: true})
				Expect(err).To(MatchError(ContainSubstring(branch)))
			}
			Expect(lines(git(source, "worktree", "list"))).To(HaveLen(1))
		})
		It("removes the worktree on release unless kept", func() {
			source := initRepo()
			ws, err := Prepare(ctx, Options{Source: source, Kind: KindClean, BaseDir: baseDir})
			Expect(err).NotTo(HaveOccurred())
			Expect(ws.Release()).To(Succeed())
			Expect(ws.Dir).NotTo(BeADirectory())

			kept, err := Prepare(ctx, Options{Source: source, Kind: KindClean, BaseDir: baseDir, Keep: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(kept.Release()).To(Succeed())
			Expect(kept.Dir).To(BeADirectory())
		})
		It("requires a base directory or an explicit path", func() {
			_, err := Prepare(ctx, Options{Source: initRepo(), Kind: KindClean})
			Expect(err).To(MatchError(ContainSubstring("BaseDir")))
		})
	})

	Describe("dirty", func() {
		It("carries staged, unstaged and untracked work without touching the source", func() {
			source := dirtyRepo()
			before := git(source, "status", "--porcelain")
			ws := prepare(Options{Source: source, Kind: KindDirty, BaseDir: baseDir})

			Expect(git(ws.Dir, "diff", "--cached", "--name-only")).To(Equal("staged.txt"))
			Expect(git(ws.Dir, "diff", "--name-only")).To(Equal("seed.txt"))
			Expect(git(ws.Dir, "ls-files", "--others", "--exclude-standard")).To(Equal("untracked.txt"))
			Expect(ws.Carried).To(ConsistOf("seed.txt", "staged.txt", "untracked.txt"))
			Expect(filepath.Join(ws.Dir, "ignored.txt")).NotTo(BeAnExistingFile())
			Expect(git(source, "status", "--porcelain")).To(Equal(before))
		})
	})

	It("rides on the context", func() {
		ws := prepare(Options{Source: initRepo(), Kind: KindHead})
		got, ok := FromContext(NewContext(ctx, ws))
		Expect(ok).To(BeTrue())
		Expect(got).To(BeIdenticalTo(ws))
		_, ok = FromContext(ctx)
		Expect(ok).To(BeFalse())
	})

	It("never removes the source checkout of a head workspace", func() {
		source := initRepo()
		ws := prepare(Options{Source: source, Kind: KindHead})
		Expect(ws.Release()).To(Succeed())
		_, err := os.Stat(filepath.Join(source, ".git"))
		Expect(err).NotTo(HaveOccurred())
	})
})
