# Local source preparation

`Cache.PrepareSource` captures the source a local analysis tool should read and the exact changed-path selection it should report against. It uses the existing `commons-db/git` command, environment, path validation and timing infrastructure. It does not require a remote or provider client.

Live worktree comparison requires Git 2.45 or newer for `--no-lazy-fetch`. Older versions fail explicitly rather than fetching missing objects on demand. Immutable sources use the package's existing Git 2.31 minimum.

```go
cache := &git.Cache{Root: filepath.Join(project, ".tmp", "sources")}
source, err := cache.PrepareSource(ctx, git.SourceOptions{
    Directory: project,
    Kind:      git.SourceCommit,
    Revision:  "main...feature",
})
if err != nil {
    return err
}
defer func() { err = errors.Join(err, source.Release()) }()

info := source.Info()
// Read the full analysis context from info.Dir. Report against info.RepositoryRoot.
// source.Changes() is the exact report selection, including deleted paths.
```

The cleanup example assumes the enclosing function returns a named `err`. Callers must retain and report cleanup failures. Gavel and UIR can consume this API without depending on each other's source preparation or CLI packages.

## Request

| Option | Meaning |
| --- | --- |
| `Cache.Root` | Required scratch parent. A relative path resolves against the process working directory. Each immutable preparation allocates its own directory. Use the project's `.tmp/` for development tooling. |
| `Directory` | Required existing directory inside the selected local checkout. Linked worktrees use their own HEAD and index. Bare repositories are rejected. |
| `Kind` | Required `SourceWorktree`, `SourceCommit`, or `SourceStaged`. There is no implicit default. |
| `Revision` | Required for `SourceCommit`, forbidden otherwise. Select one commit expression, an endpoint range `A..B`, or a merge-base range `A...B`. Omitted endpoints mean HEAD. |

## Selection and contents

| Kind | Changed paths | Directory contents |
| --- | --- | --- |
| `SourceWorktree` | Net tracked differences from HEAD plus nonignored untracked files. Before the first commit, existing indexed files and nonignored untracked files are additions. | Original live checkout; no scratch allocation. Concurrent edits remain visible. |
| `SourceCommit` with one revision | First parent to target; a root commit compares against an empty tree. | Entire target commit, independent of the current checkout and sparse selection. |
| `SourceCommit` with `A..B` | Endpoint A to endpoint B. | Entire B. |
| `SourceCommit` with `A...B` | Unique merge base of A and B to B. Unrelated histories and multiple merge bases fail. | Entire B. |
| `SourceStaged` | Captured HEAD to the captured index tree; before the first commit, empty tree to index. Intent-to-add entries are omitted. Unmerged entries fail. | Entire captured index, including staged configuration and manifests. Later index or worktree edits do not change the prepared source. |

Changes are sorted by literal, slash-separated repository-relative path. Status uses the existing `FileStatus` values `A`, `M`, `D` and `T`. Renames produce deletion and addition entries. Filenames containing tabs, newlines or glob characters remain literal. `Changes()` returns a defensive copy and returns an explicit empty slice when there are no changes. Consumers must handle an empty selection explicitly: passing it to an API that interprets an empty filter as unrestricted would lint every file.

Analysis should read the full tree and apply the selection to reported findings afterward. Changes to manifests or configuration are returned like other files; deciding whether they expand the reporting scope belongs to the consumer.

## Metadata and lifecycle

`Info()` returns value metadata: `RepositoryRoot` is the canonical original checkout root and `Dir` is where to read. `BaseCommit` is the comparison baseline, empty for an initial commit or unborn checkout. `TargetCommit` is present only for commit sources. `TreeID` is the repository-native SHA-1 or SHA-256 tree ID for commit and staged sources; a live worktree has no immutable tree ID. A staged tree ID is not a commit ID.

Commit and staged directories are plain materialized trees with no Git metadata. Consumers must use the supplied provenance rather than running Git discovery from `Dir`, which might otherwise discover an enclosing checkout. Once preparation succeeds, all source files are materialized and remain readable even if the original object database changes. The directory is isolated from the original checkout but is writable by its owner; consumers must treat it as immutable.

`Release()` removes only the allocation owned by this preparation. It is idempotent, serializes concurrent releases, and does not depend on a canceled request context. A cleanup failure remains retryable. Releasing a live worktree never deletes caller files. Preparation failures clean up their own partial allocations and join any cleanup error to the original error.

## Isolation and limits

Immutable sources read existing local Git objects through a private repository and write temporary index/tree objects only there. They do not fetch, create synthetic commits, update refs, register worktrees, or write the original index or object database. Split and sparse indexes and native SHA-256 repositories are supported. Staged capture reads one index version and checks that HEAD did not change during capture; an index changed afterward does not invalidate that captured version.

Materialization reads raw blobs. Executable permissions and safe relative symlinks are preserved. Checkout filters, line-ending conversion, archive attributes and LFS hydration are not applied. Gitlinks/submodules, absolute or escaping symlinks, symlink loops and unsupported tree modes fail explicitly. Missing local objects fail rather than triggering a fetch. Filesystems that cannot represent a tree's paths or modes produce an error.

## Errors

Use `errors.Is` with `ErrInvalidSource`, `ErrRevisionNotFound`, `ErrUnmergedIndex`, `ErrSourceChanged`, `ErrUnsupportedSource`, `ErrNotRepository` or `ErrUnrelatedHistories`. Other Git, context, filesystem and cleanup errors remain wrapped with context. `PrepareSource` returns a nil source on failure. No process-global Git configuration or environment is changed.

## Verification

Run the integration tests against temporary local repositories:

```sh
go test ./git -ginkgo.focus='Prepared sources' -ginkgo.no-color
go test ./git
```
