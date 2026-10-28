package workspace

import (
	"errors"
	"path/filepath"

	dbcontext "github.com/flanksource/commons-db/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// recordingGate captures every Check it sees and blocks when block is set.
type recordingGate struct {
	checks []Check
	block  bool
}

func (g *recordingGate) Name() string { return "recording" }

func (g *recordingGate) Check(_ dbcontext.Context, _ *Workspace, c Check) error {
	g.checks = append(g.checks, c)
	if g.block {
		return &GateError{Gate: g.Name(), Phase: c.Phase, Findings: c.Paths}
	}
	return nil
}

// appendHook appends a line to a file, standing in for a formatter.
type appendHook struct{ path, line string }

func (h appendHook) Name() string { return "append" }

func (h appendHook) Run(_ dbcontext.Context, ws *Workspace, _ []string) error {
	write(ws.Dir, h.path, read(ws.Dir, h.path)+h.line)
	return nil
}

var _ = Describe("Commit/Push pipeline", func() {
	var ctx dbcontext.Context
	BeforeEach(func() { ctx = dbcontext.New() })

	prepare := func(opts Options) *Workspace {
		if opts.Kind != KindHead && opts.BaseDir == "" {
			opts.BaseDir = GinkgoT().TempDir()
		}
		ws, err := Prepare(ctx, opts)
		ExpectWithOffset(1, err).NotTo(HaveOccurred())
		DeferCleanup(ws.Release)
		return ws
	}
	commitFiles := func(dir, rev string) []string {
		return lines(git(dir, "diff-tree", "--no-commit-id", "--name-only", "-r", rev))
	}

	// The PR-branch leak: the source ignores .npmrc and vendor/, the branch the
	// worktree is cut from ignores neither.
	leakyWorktree := func() *Workspace {
		source := initRepo()
		git(source, "branch", "pr/old")
		write(source, ".gitignore", "ignored.txt\nvendor/\n.npmrc\n")
		git(source, "commit", "-q", "-am", "ignore npmrc")
		write(source, ".npmrc", "//registry.npmjs.org/:_authToken=npm_example\n")
		write(source, "vendor/nested/dep.txt", "dep\n")
		return prepare(Options{Source: source, Kind: KindClean, Base: "pr/old", Ignored: true})
	}

	Describe("manifest", func() {
		It("records compact excludes and reopens them from the directory", func() {
			ws := leakyWorktree()
			Expect(ws.Ignored).To(ConsistOf(".npmrc", "vendor/"))
			Expect(ws.IsExcluded("vendor/nested/dep.txt")).To(BeTrue())
			Expect(ws.IsExcluded("vendorish.txt")).To(BeFalse())

			opened, err := Open(ctx, filepath.Join(ws.Dir, "vendor"))
			Expect(err).NotTo(HaveOccurred())
			Expect(opened.Dir).To(Equal(ws.Dir))
			Expect(opened.Kind).To(Equal(KindClean))
			Expect(opened.Ignored).To(Equal(ws.Ignored))
			Expect(opened.Base).To(Equal(ws.Base))
			Expect(git(ws.Dir, "status", "--porcelain")).NotTo(ContainSubstring("workspace.json"))
		})
		It("keeps a path the branch tracks out of the excludes even inside an ignored directory", func() {
			source := initRepo()
			git(source, "checkout", "-q", "-b", "shell/tracks-vendor")
			write(source, "vendor/tracked.txt", "tracked on branch\n")
			git(source, "add", "-f", "vendor/tracked.txt")
			git(source, "commit", "-q", "-m", "track vendor file")
			git(source, "checkout", "-q", "main")
			write(source, "vendor/tracked.txt", "source copy\n")
			write(source, "vendor/dep.txt", "dep\n")

			ws := prepare(Options{Source: source, Kind: KindClean, Branch: "shell/tracks-vendor", Existing: true, Ignored: true})
			Expect(ws.Ignored).To(ConsistOf("vendor/dep.txt"))
			Expect(ws.IsExcluded("vendor/tracked.txt")).To(BeFalse())
		})
		It("opens a plain checkout as a head workspace with no excludes", func() {
			source := initRepo()
			ws, err := Open(ctx, source)
			Expect(err).NotTo(HaveOccurred())
			Expect(ws.Kind).To(Equal(KindHead))
			Expect(ws.Ignored).To(BeEmpty())
		})
	})

	Describe("Changes", func() {
		It("lists uncommitted paths except excluded ones", func() {
			ws := leakyWorktree()
			write(ws.Dir, "agent.txt", "agent\n")
			Expect(ws.Changes(ctx)).To(ConsistOf("agent.txt"))
		})
	})

	Describe("Commit", func() {
		It("commits the workspace's changes but never excluded paths", func() {
			ws := leakyWorktree()
			write(ws.Dir, "agent.txt", "agent\n")
			result, err := ws.Commit(ctx, CommitOptions{Message: "agent work"})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Paths).To(ConsistOf("agent.txt"))
			Expect(commitFiles(ws.Dir, result.SHA)).To(ConsistOf("agent.txt"))
		})
		It("refuses explicitly requested or pre-staged excluded paths with the built-in gate", func() {
			ws := leakyWorktree()
			head := git(ws.Dir, "rev-parse", "HEAD")
			git(ws.Dir, "add", ".npmrc")
			write(ws.Dir, "agent.txt", "agent\n")
			_, err := ws.Commit(ctx, CommitOptions{Message: "agent work", Paths: []string{"agent.txt"}})
			var gateErr *GateError
			Expect(errors.As(err, &gateErr)).To(BeTrue(), "got %v", err)
			Expect(gateErr).To(Equal(&GateError{Gate: ExcludesGateName, Phase: PhaseCommit, Findings: []string{".npmrc"}}))
			Expect(git(ws.Dir, "rev-parse", "HEAD")).To(Equal(head))
		})
		It("reports nothing to commit when only excluded paths changed", func() {
			ws := leakyWorktree()
			_, err := ws.Commit(ctx, CommitOptions{Message: "nothing"})
			Expect(err).To(MatchError(ErrNothingStaged))
		})
		It("restages hook output and shows gates the final staged set", func() {
			ws := prepare(Options{Source: initRepo(), Kind: KindHead})
			write(ws.Dir, "seed.txt", "edited\n")
			gate := &recordingGate{}
			result, err := ws.Commit(ctx, CommitOptions{
				Message: "edit", Hooks: []Hook{appendHook{path: "seed.txt", line: "formatted\n"}}, Gates: []Gate{gate},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(git(ws.Dir, "show", result.SHA+":seed.txt")).To(Equal("edited\nformatted"))
			Expect(gate.checks).To(HaveLen(1))
			Expect(gate.checks[0].Phase).To(Equal(PhaseCommit))
			Expect(gate.checks[0].Paths).To(Equal([]string{"seed.txt"}))
			Expect(git(ws.Dir, "status", "--porcelain")).To(BeEmpty())
		})
		It("leaves HEAD and the change in place when a configured gate blocks", func() {
			ws := prepare(Options{Source: initRepo(), Kind: KindHead})
			head := git(ws.Dir, "rev-parse", "HEAD")
			write(ws.Dir, "seed.txt", "edited\n")
			_, err := ws.Commit(ctx, CommitOptions{Message: "edit", Gates: []Gate{&recordingGate{block: true}}})
			Expect(err).To(MatchError(ContainSubstring("recording")))
			Expect(git(ws.Dir, "rev-parse", "HEAD")).To(Equal(head))
			Expect(read(ws.Dir, "seed.txt")).To(Equal("edited\n"))
		})
		It("only applies secret-name and size gates when they are configured", func() {
			ws := prepare(Options{Source: initRepo(), Kind: KindHead})
			write(ws.Dir, ".env", "TOKEN=example\n")
			gates, err := ResolveGates([]GateSpec{{Name: SecretNamesGateName}, {Name: MaxSizeGateName, Config: map[string]any{"bytes": 4}}})
			Expect(err).NotTo(HaveOccurred())

			_, err = ws.Commit(ctx, CommitOptions{Message: "env", Gates: gates})
			Expect(err).To(MatchError(ContainSubstring(".env")))

			write(ws.Dir, "big.txt", "more than four bytes\n")
			sizeOnly, err := ResolveGates([]GateSpec{{Name: MaxSizeGateName, Config: map[string]any{"bytes": 4}}})
			Expect(err).NotTo(HaveOccurred())
			_, err = ws.Commit(ctx, CommitOptions{Message: "big", Paths: []string{"big.txt"}, Gates: sizeOnly})
			Expect(err).To(MatchError(ContainSubstring("big.txt")))

			result, err := ws.Commit(ctx, CommitOptions{Message: "unconfigured"})
			Expect(err).NotTo(HaveOccurred())
			Expect(commitFiles(ws.Dir, result.SHA)).To(ConsistOf(".env", "big.txt"))
		})
		It("rejects an unknown gate name", func() {
			_, err := ResolveGates([]GateSpec{{Name: "nope"}})
			Expect(err).To(MatchError(ContainSubstring(`unknown gate "nope"`)))
		})
		It("cuts fixup and amend commits", func() {
			ws := prepare(Options{Source: initRepo(), Kind: KindHead})
			write(ws.Dir, "a.txt", "a\n")
			first, err := ws.Commit(ctx, CommitOptions{Message: "add a", Trailers: []string{"Gavel-Issue-Id: 123"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(git(ws.Dir, "log", "-1", "--format=%B")).To(ContainSubstring("Gavel-Issue-Id: 123"))

			write(ws.Dir, "a.txt", "a2\n")
			fixup, err := ws.Commit(ctx, CommitOptions{Mode: CommitFixup, Anchor: first.SHA})
			Expect(err).NotTo(HaveOccurred())
			Expect(git(ws.Dir, "log", "-1", "--format=%s")).To(Equal("fixup! add a"))

			write(ws.Dir, "a.txt", "a3\n")
			amended, err := ws.Commit(ctx, CommitOptions{Mode: CommitAmend})
			Expect(err).NotTo(HaveOccurred())
			Expect(git(ws.Dir, "rev-parse", amended.SHA+"^")).To(Equal(first.SHA))
			Expect(amended.SHA).NotTo(Equal(fixup.SHA))
			Expect(git(ws.Dir, "show", "HEAD:a.txt")).To(Equal("a3"))
		})
		It("requires an anchor for a fixup", func() {
			ws := prepare(Options{Source: initRepo(), Kind: KindHead})
			write(ws.Dir, "a.txt", "a\n")
			_, err := ws.Commit(ctx, CommitOptions{Mode: CommitFixup})
			Expect(err).To(MatchError(ContainSubstring("Anchor")))
		})
	})

	Describe("edge cases", func() {
		It("opens and commits in a repository with no commits yet", func() {
			dir, err := filepath.EvalSymlinks(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			git(dir, "init", "-q", "-b", "main")
			git(dir, "config", "user.email", "test@example.com")
			git(dir, "config", "user.name", "Test")
			write(dir, "first.txt", "first\n")

			ws, err := Open(ctx, dir)
			Expect(err).NotTo(HaveOccurred())
			Expect(ws.Base).To(BeEmpty())
			result, err := ws.Commit(ctx, CommitOptions{Message: "root"})
			Expect(err).NotTo(HaveOccurred())
			Expect(git(dir, "ls-tree", "--name-only", result.SHA)).To(Equal("first.txt"))
		})
		It("commits a deletion already staged with git rm", func() {
			ws := prepare(Options{Source: initRepo(), Kind: KindHead})
			git(ws.Dir, "rm", "-q", "seed.txt")
			result, err := ws.Commit(ctx, CommitOptions{Message: "drop seed", Paths: []string{"seed.txt"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(git(ws.Dir, "show", "--name-status", "--format=", result.SHA)).To(Equal("D\tseed.txt"))
		})
	})

	Describe("Push", func() {
		var source, remote string
		BeforeEach(func() {
			source = initRepo()
			remote = filepath.Join(GinkgoT().TempDir(), "remote.git")
			git(source, "init", "-q", "--bare", "-b", "main", remote)
			git(source, "push", "-q", remote, "main")
		})

		It("shows gates every path touched in the outgoing range, including history the net diff hides", func() {
			ws := prepare(Options{Source: source, Kind: KindHead})
			tip := git(source, "rev-parse", "HEAD")
			write(source, "secret.txt", "token\n")
			_, err := ws.Commit(ctx, CommitOptions{Message: "add"})
			Expect(err).NotTo(HaveOccurred())
			Expect(rmFile(source, "secret.txt")).To(Succeed())
			write(source, "kept.txt", "kept\n")
			_, err = ws.Commit(ctx, CommitOptions{Message: "remove"})
			Expect(err).NotTo(HaveOccurred())

			gate := &recordingGate{}
			Expect(ws.Push(ctx, PushOptions{Remote: remote, Ref: "refs/heads/main", Gates: []Gate{gate}})).To(Succeed())
			Expect(gate.checks).To(HaveLen(1))
			Expect(gate.checks[0]).To(Equal(Check{
				Phase: PhasePush, From: tip, To: git(source, "rev-parse", "HEAD"),
				Paths:   []string{"kept.txt", "secret.txt"},
				Changes: []Change{{Status: StatusAdded, Path: "kept.txt"}},
			}))
			Expect(git(remote, "rev-parse", "main")).To(Equal(git(source, "rev-parse", "HEAD")))
		})
		It("sends nothing when a gate blocks", func() {
			ws := prepare(Options{Source: source, Kind: KindHead})
			tip := git(remote, "rev-parse", "main")
			write(source, "x.txt", "x\n")
			_, err := ws.Commit(ctx, CommitOptions{Message: "x"})
			Expect(err).NotTo(HaveOccurred())
			err = ws.Push(ctx, PushOptions{Remote: remote, Ref: "refs/heads/main", Gates: []Gate{&recordingGate{block: true}}})
			Expect(err).To(HaveOccurred())
			Expect(git(remote, "rev-parse", "main")).To(Equal(tip))
		})
		It("refuses to overwrite a remote that moved past the lease", func() {
			ws := prepare(Options{Source: source, Kind: KindHead})
			stale := git(remote, "rev-parse", "main")
			other := filepath.Join(GinkgoT().TempDir(), "other")
			git(source, "clone", "-q", remote, other)
			git(other, "config", "user.email", "test@example.com")
			git(other, "config", "user.name", "Test")
			git(other, "commit", "-q", "--allow-empty", "-m", "moved")
			git(other, "push", "-q", "origin", "main")
			moved := git(remote, "rev-parse", "main")

			write(source, "x.txt", "x\n")
			_, err := ws.Commit(ctx, CommitOptions{Message: "x"})
			Expect(err).NotTo(HaveOccurred())
			err = ws.Push(ctx, PushOptions{Remote: remote, Ref: "refs/heads/main", Lease: stale})
			Expect(err).To(HaveOccurred())
			Expect(git(remote, "rev-parse", "main")).To(Equal(moved))
		})
		It("rejects a short ref name", func() {
			ws := prepare(Options{Source: source, Kind: KindHead})
			Expect(ws.Push(ctx, PushOptions{Remote: remote, Ref: "main"})).To(MatchError(ContainSubstring("full ref")))
		})
	})
})
