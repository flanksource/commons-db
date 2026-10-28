# workspace

`workspace` prepares the directory a run works in from a source git checkout, and syncs folder state between directories using git as the transport.

`shell.Prepare` / `shell.SetupEnv` build every checkout on it, so `SetupResult.Workspace` is set whenever a setup has a `checkout`.

## Kinds

Each kind is an `Adapter` registered under its `Kind`:

| Kind | Directory | Carries |
|---|---|---|
| `head` | the source checkout itself, in place | everything it already has |
| `clean` | a new `git worktree` at `Base` (or an `Existing` branch) | nothing from the source's uncommitted work |
| `dirty` | a new `git worktree` | the source's staged, unstaged and untracked work (`Workspace.Carried`) |

With `Ignored: true`, worktree kinds also clone the source's gitignored content (`node_modules/`, build caches, `.env`) into the worktree. Clones are copy-on-write where the filesystem supports it (APFS `clonefile`, btrfs/XFS `FICLONE`) and byte copies otherwise; files are never hardlinked. Paths the worktree's branch tracks keep the branch's content.

The cloned content is recorded in `Workspace.Ignored` as compact entries: a file, or a whole directory such as `vendor/`. A directory is expanded into its files when the branch tracks something inside it. `ws.IsExcluded(path)` keeps that content out of every commit, snapshot and push, even when the worktree's branch doesn't ignore it. That is the case that leaked `.npmrc` from an old PR branch.

Worktree kinds write a manifest to their own git directory (`.git/worktrees/<name>/workspace.json`, never committed). `workspace.Open(ctx, dir)` reopens it from any directory inside the worktree, so a later hook or process sees the same excludes. Any other checkout opens as a `head` workspace with no excludes. `ws.Changes(ctx)` lists uncommitted paths minus excluded ones.

```go
ws, err := workspace.Prepare(ctx, workspace.Options{
	Source:  ".",
	Kind:    workspace.KindDirty,
	BaseDir: ".shell",
	Ignored: true,
})
if err != nil {
	return err
}
defer ws.Release() // removes the worktree unless Keep is set

ctx = workspace.NewContext(ctx, ws) // workspace.FromContext(ctx) reads it back
```

## Snapshots

`ws.Snapshot(ctx, name)` commits the working state to `refs/snapshots/<name>`. The snapshot holds tracked and untracked files. It never holds gitignored files, or anything listed in `Workspace.Ignored`, even when the worktree's branch doesn't ignore them. The commit is built in a private index, so HEAD, the index, the working tree and the stash are never touched.

- The first parent is `Base`, the HEAD the state is relative to.
- The second parent is the previous snapshot under the same name, so successive snapshots form a chain that fast-forwards.
- An unchanged state returns the existing snapshot rather than cutting a new one.

## Commit and push pipeline

Every commit and push made through a workspace runs one pipeline:

- `ws.Commit`: stage, then hooks, then restage, then gates, then commit.
- `ws.Push`: compute what's outgoing, then gates, then push.

```go
gates, err := workspace.ResolveGates([]workspace.GateSpec{
	{Name: workspace.SecretNamesGateName},
	{Name: workspace.MaxSizeGateName, Config: map[string]any{"bytes": 1 << 20}},
})

result, err := ws.Commit(ctx, workspace.CommitOptions{
	Message:  "fix: handle empty input",
	Trailers: []string{"Gavel-Issue-Id: 123"},
	Hooks:    []workspace.Hook{formatter}, // may rewrite files; the pipeline restages
	Gates:    gates,
})

err = ws.Push(ctx, workspace.PushOptions{
	Remote: "https://example.com/acme/app.git",
	Ref:    "refs/heads/fix/empty-input",
	Lease:  expectedRemoteSHA, // --force-with-lease against exactly this SHA
	Gates:  gates,
})
```

**Built in.** The `excludes` gate is the only one, and it always runs. It refuses excluded paths at commit and at push.

**Configured.** Every other gate runs only when it's listed:
- `secret-names` refuses credential file names (`.env`, `.npmrc`, `*.pem`, …).
- `max-size` refuses blobs over `config.bytes`.

Hosts register their own gates (linters, secret scanners) with `workspace.RegisterGate`. An unknown name fails `ResolveGates`.

**Commit details.**
- `Mode` is `commit` (default), `fixup` (needs `Anchor`) or `amend`.
- With `Paths` nil, every `Change` is committed.
- An empty filtered set returns `ErrNothingStaged`.
- Git's own hooks are skipped unless `GitHooks` is set, since the pipeline is the hook mechanism.
- A refusing gate returns a `*GateError` naming what it blocked, and leaves HEAD untouched.

**What push gates see.**
- `Check.Paths` is every path any commit in the outgoing range touched. That includes a file added and removed again, because it still travels in history.
- `Check.Changes` is the net diff.
- The range starts at `From`, else `Lease`, else the remote tip. When the remote tip isn't known locally, the gates inspect everything `Source` carries.
- A refusing gate sends nothing.

## Push, pull and diff of snapshots

```go
snap, err := ws.Snapshot(ctx, "laptop")
err = ws.Push(ctx, workspace.PushOptions{Remote: "git@example.com:acme/sync.git", Ref: snap.Ref, Source: snap.Commit})

// elsewhere, in a checkout of the same repository at snap.Base
pulled, err := other.Pull(ctx, workspace.PullOptions{Remote: "git@example.com:acme/sync.git", Name: "laptop"})

// what changed between two snapshots, or against the current working state ("")
diff, err := ws.Diff(ctx, workspace.DiffOptions{From: pulled.Commit, Patch: true})
```

- `Remote` is a remote name, URL or repository path. Leave it empty to pull between worktrees of one repository, since they share refs.
- Pull writes and removes exactly the files that differ, leaves gitignored content alone, and moves neither HEAD nor the index. The pulled state shows as uncommitted work.
- Pull is fast-forward only. HEAD must be the snapshot's `Base`, and the working state must be clean or equal to a snapshot this one descends from. Otherwise it fails and names the local changes that would be lost. `Force: true` overwrites them.
- A push is a plain fast-forward of `refs/snapshots/<name>`. A diverged remote snapshot is rejected, never overwritten.
