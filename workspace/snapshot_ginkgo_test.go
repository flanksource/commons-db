package workspace

import (
	"path/filepath"

	dbcontext "github.com/flanksource/commons-db/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gstruct"
)

var _ = Describe("Snapshots", func() {
	var ctx dbcontext.Context
	BeforeEach(func() { ctx = dbcontext.New() })

	prepare := func(opts Options) *Workspace {
		if opts.BaseDir == "" && opts.Kind != KindHead {
			opts.BaseDir = GinkgoT().TempDir()
		}
		ws, err := Prepare(ctx, opts)
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		DeferCleanup(ws.Release)
		return ws
	}
	snapshot := func(ws *Workspace, name string) *Snapshot {
		snap, err := ws.Snapshot(ctx, name)
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		return snap
	}
	treeFiles := func(dir, rev string) []string {
		return lines(git(dir, "ls-tree", "-r", "--name-only", rev))
	}

	Describe("Snapshot", func() {
		It("captures tracked and untracked work but never ignored content", func() {
			source := dirtyRepo()
			ws := prepare(Options{Source: source, Kind: KindHead})
			before := git(source, "status", "--porcelain")
			head := git(source, "rev-parse", "HEAD")

			snap := snapshot(ws, "wip")

			Expect(snap).To(gstruct.PointTo(gstruct.MatchFields(gstruct.IgnoreExtras, gstruct.Fields{
				"Name": Equal("wip"), "Ref": Equal("refs/snapshots/wip"), "Base": Equal(head), "Previous": BeEmpty(),
			})))
			Expect(treeFiles(source, snap.Commit)).To(ConsistOf(".gitignore", "seed.txt", "staged.txt", "untracked.txt"))
			Expect(git(source, "show", snap.Commit+":seed.txt")).To(Equal("seed\nunstaged"))
			Expect(git(source, "rev-parse", "refs/snapshots/wip")).To(Equal(snap.Commit))
			Expect(git(source, "rev-parse", "HEAD")).To(Equal(head))
			Expect(git(source, "status", "--porcelain")).To(Equal(before), "index and working tree are untouched")
		})

		// The PR-branch leak: a file the source ignores is cloned into a
		// worktree whose branch does not ignore it.
		It("excludes cloned ignored files even where the worktree's branch does not ignore them", func() {
			source := initRepo()
			git(source, "branch", "pr/old")
			write(source, ".gitignore", "ignored.txt\nvendor/\n.npmrc\n")
			git(source, "commit", "-q", "-am", "ignore npmrc")
			write(source, ".npmrc", "//registry.npmjs.org/:_authToken=npm_example\n")

			ws := prepare(Options{Source: source, Kind: KindClean, Base: "pr/old", Ignored: true})
			Expect(read(ws.Dir, ".npmrc")).To(ContainSubstring("npm_example"))
			Expect(git(ws.Dir, "ls-files", "--others", "--exclude-standard")).To(Equal(".npmrc"), "the branch sees it as untracked")
			write(ws.Dir, "agent.txt", "agent work\n")

			snap := snapshot(ws, "turn")
			Expect(treeFiles(ws.Dir, snap.Commit)).To(ConsistOf(".gitignore", "seed.txt", "agent.txt"))
		})

		It("chains successive snapshots and reuses an unchanged one", func() {
			source := initRepo()
			ws := prepare(Options{Source: source, Kind: KindHead})
			write(source, "one.txt", "one\n")
			first := snapshot(ws, "sync")
			Expect(snapshot(ws, "sync")).To(Equal(first))

			write(source, "two.txt", "two\n")
			second := snapshot(ws, "sync")
			Expect(second.Previous).To(Equal(first.Commit))
			Expect(git(source, "rev-parse", second.Commit+"^2")).To(Equal(first.Commit))
		})

		It("rejects an invalid snapshot name", func() {
			ws := prepare(Options{Source: initRepo(), Kind: KindHead})
			_, err := ws.Snapshot(ctx, "bad..name")
			Expect(err).To(MatchError(ContainSubstring("bad..name")))
		})
	})

	Describe("Push and Pull", func() {
		var source, remote, clone string
		BeforeEach(func() {
			source = initRepo()
			remote = filepath.Join(GinkgoT().TempDir(), "sync.git")
			git(source, "init", "-q", "--bare", "-b", "main", remote)
			git(source, "push", "-q", remote, "main")
			clone = filepath.Join(GinkgoT().TempDir(), "clone")
			git(source, "clone", "-q", remote, clone)
		})
		pushSnapshot := func(ws *Workspace, snap *Snapshot) error {
			return ws.Push(ctx, PushOptions{Remote: remote, Ref: snap.Ref, Source: snap.Commit})
		}

		It("syncs a folder's state through a remote, including deletions", func() {
			from := prepare(Options{Source: source, Kind: KindHead})
			write(source, "new.txt", "new\n")
			write(source, "seed.txt", "edited\n")
			write(source, "ignored.txt", "stays local\n")
			snap := snapshot(from, "laptop")
			Expect(pushSnapshot(from, snap)).To(Succeed())

			to := prepare(Options{Source: clone, Kind: KindHead})
			pulled, err := to.Pull(ctx, PullOptions{Remote: remote, Name: "laptop"})
			Expect(err).NotTo(HaveOccurred())
			Expect(pulled.Commit).To(Equal(snap.Commit))
			Expect(read(clone, "new.txt")).To(Equal("new\n"))
			Expect(read(clone, "seed.txt")).To(Equal("edited\n"))
			Expect(filepath.Join(clone, "ignored.txt")).NotTo(BeAnExistingFile())
			Expect(git(clone, "rev-parse", "HEAD")).To(Equal(snap.Base), "HEAD is not moved")

			Expect(rmFile(source, "new.txt")).To(Succeed())
			next := snapshot(from, "laptop")
			Expect(pushSnapshot(from, next)).To(Succeed())
			_, err = to.Pull(ctx, PullOptions{Remote: remote, Name: "laptop"})
			Expect(err).NotTo(HaveOccurred())
			Expect(filepath.Join(clone, "new.txt")).NotTo(BeAnExistingFile())
		})

		It("syncs between worktrees of one repository without a remote", func() {
			from := prepare(Options{Source: source, Kind: KindHead})
			write(source, "shared.txt", "shared\n")
			snapshot(from, "handoff")

			to := prepare(Options{Source: source, Kind: KindClean})
			_, err := to.Pull(ctx, PullOptions{Name: "handoff"})
			Expect(err).NotTo(HaveOccurred())
			Expect(read(to.Dir, "shared.txt")).To(Equal("shared\n"))
		})

		It("refuses to overwrite local changes unless forced", func() {
			from := prepare(Options{Source: source, Kind: KindHead})
			write(source, "seed.txt", "remote edit\n")
			Expect(pushSnapshot(from, snapshot(from, "laptop"))).To(Succeed())

			to := prepare(Options{Source: clone, Kind: KindHead})
			write(clone, "seed.txt", "local edit\n")
			_, err := to.Pull(ctx, PullOptions{Remote: remote, Name: "laptop"})
			Expect(err).To(MatchError(And(ContainSubstring("not a fast-forward"), ContainSubstring("seed.txt"))))
			Expect(read(clone, "seed.txt")).To(Equal("local edit\n"))

			_, err = to.Pull(ctx, PullOptions{Remote: remote, Name: "laptop", Force: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(read(clone, "seed.txt")).To(Equal("remote edit\n"))
		})

		It("refuses a snapshot taken on a different base commit", func() {
			from := prepare(Options{Source: source, Kind: KindHead})
			write(source, "later.txt", "later\n")
			git(source, "add", "later.txt")
			git(source, "commit", "-q", "-m", "later")
			write(source, "wip.txt", "wip\n")
			Expect(pushSnapshot(from, snapshot(from, "laptop"))).To(Succeed())

			to := prepare(Options{Source: clone, Kind: KindHead})
			_, err := to.Pull(ctx, PullOptions{Remote: remote, Name: "laptop"})
			Expect(err).To(MatchError(ContainSubstring("based on")))
		})
	})

	Describe("Diff", func() {
		It("reports changes between snapshots and against the working state", func() {
			source := initRepo()
			ws := prepare(Options{Source: source, Kind: KindHead})
			write(source, "added.txt", "added\n")
			first := snapshot(ws, "d")
			write(source, "seed.txt", "changed\n")
			Expect(rmFile(source, "added.txt")).To(Succeed())
			second := snapshot(ws, "d")

			diff, err := ws.Diff(ctx, DiffOptions{From: first.Commit, To: second.Commit, Patch: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(diff.Changes).To(ConsistOf(
				Change{Status: StatusDeleted, Path: "added.txt"},
				Change{Status: StatusModified, Path: "seed.txt"},
			))
			Expect(diff.Patch).To(ContainSubstring("+changed"))

			write(source, "fresh.txt", "fresh\n")
			working, err := ws.Diff(ctx, DiffOptions{From: second.Commit})
			Expect(err).NotTo(HaveOccurred())
			Expect(working.Changes).To(Equal([]Change{{Status: StatusAdded, Path: "fresh.txt"}}))
			Expect(working.Patch).To(BeEmpty())
		})
	})
})
