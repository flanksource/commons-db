# Immutable repository reads

`Cache` uses its existing bare mirror for history, commit counts, tree changes, selected files and supplied-content blame. These reads require Git 2.42 or newer. They do not lease worktrees.

Resolve a branch or tag to a full commit SHA when the caller needs several operations to describe the same revision. An operation lists remote refs once for named references. Full SHAs use cached commits without remote discovery; missing commits are fetched. History and blame complete shallow ancestry when necessary. Tree changes and blob reads only need the selected endpoint objects.

## Compare first, load on selection

```go
changes, err := cache.Changes(ctx, git.ChangesOptions{
    Repository: repo, // or Directory: "/path/to/local/repository"
    Base:       baseSHA,
    Head:       headSHA,
    Paths:      []string{"rules/"},
    Renames:    true,
})
if err != nil {
    return err
}
for _, change := range changes {
    // Equal object IDs mean only the mode changed.
    if change.OldObject == change.NewObject {
        continue
    }
    fmt.Println(change.Status, change.Path)
}

blobs, err := cache.ReadBlobs(ctx, repo, []git.BlobRequest{
    {SHA: baseSHA, Path: selectedPath},
    {SHA: headSHA, Path: selectedPath},
})
if err != nil {
    return err
}
```

`Changes` compares two trees using native Git metadata and returns literal paths, old/new object IDs and modes. `Paths` limits the comparison to literal paths (never globs). Without `Renames`, a rename appears as deletion and addition; with it (`-M`), a rename is one `FileRenamed` change carrying `OldPath`. It includes mode-only changes so consumers can decide whether those matter. `Directory` compares a local working or bare repository through the same private alternates scratch as `OpenTree`, without fetching it into a mirror.

`ReadBlobs` accepts full 40-character commit SHAs and repository-relative file paths. Results preserve request order. `Present: false` means the path is absent at that commit; a present empty file has `Present: true` and zero content bytes. Directory paths, invalid commits, malformed batch output and command failures return errors. One `cat-file --batch-command -Z` process handles the batch, including filenames containing tabs or newlines. `Show` delegates to this reader and returns `ErrPathNotFound` for an absent file.

## History details and blame

```go
commits, err := cache.History(ctx, repo, headSHA, git.HistoryOptions{
    Detail: git.HistoryMetadata,
    Limit:  51,
})
if err != nil {
    return err
}
```

`HistoryMetadata` omits file lists and rename detection. `HistoryNames` includes changed names without detecting renames. The default, `HistoryStatus`, preserves status and rename metadata. Limits, offsets, path filters and `Follow` retain their existing behavior. `Compare` uses native commit counts and merge-base calculation.

`Blame` accepts the displayed bytes, the original repository path and an immutable commit SHA. Native Git attributes those bytes directly from the bare mirror. Inserted or modified lines outside that commit have no commit attribution. No supplied bytes are written into a checkout.

## Read a commit as a filesystem

`OpenTree` exposes one commit through `fs.FS`, `fs.ReadFileFS`, and `fs.ReadDirFS`. Choose either an existing working or bare repository with `TreeOptions.Directory`, or a remote with `TreeOptions.Repository`. `Info().SHA` identifies the captured commit. Later branch and working-file changes do not affect it.

```go
tree, err := cache.OpenTree(ctx, git.TreeOptions{
    Directory: "/path/to/rules.git",
    Revision:  commitSHA,
})
if err != nil {
    return err
}
defer func() { err = errors.Join(err, tree.Close()) }()
body, err := fs.ReadFile(tree, "rules/pc/Example/data.xml")
```

Close the tree when its consumer finishes. It owns a reader slot and one lazily started `cat-file --batch-command -Z` process. Reading a file whose directory was not listed yet lists that one directory (non-recursive `ls-tree`), so reading every file of a directory costs one listing; `ReadDir` of an unlisted directory loads the whole tree once (`ls-tree -r -t`), so a walk costs one listing. Listings carry no blob sizes: an entry's `Info()` asks the object process for its size on first use. `git.ObjectID(entry)` returns a listed entry's blob or tree object ID, so a consumer can skip an unchanged subtree or reuse a parse keyed by blob ID. Local repositories use private bare metadata and object alternates in the configured cache, removed on close; revisions resolve in the local repository and none of its refs are copied. Source files are never extracted. Symlinks are listed but cannot be read as ordinary files. Cancellation terminates the owned Git process and close releases its resources.

## Local repositories

A `Repository` whose URL is a local path or `file://` URL (`RepoURL.LocalPath`) is read in place by every read API (`OpenTree`, `ReadBlobs`, `Show`, `Files`, `Changes`, `History`, `Log`, `Compare`, `MergeBase`, `Ancestors`, `Blame`): nothing is fetched and no mirror is created. `LsRemote` and `Resolve` list its refs with native `git ls-remote --symref`, so a detached HEAD, linked worktrees and many refs resolve as git itself sees them. Reads run in `<Root>/<CacheKey>/local.git`, a bare repository without refs that borrows the local repository's objects (`objects/info/alternates`) and shallow file, so the local repository's config never applies. Each commit is checked present once per process (`ErrRevisionNotFound` otherwise). A read that mixes a local repository with another one (a fork's head) uses the mirror. Writes (`Checkout`, `Apply`, push) keep mirror semantics: the local repository is a remote they fetch from and push to.

## Concurrency and retention

Reads hold the mirror lock shared, so reads proceed together while a fetch, a pin write or `Maintain` (which hold it exclusively) waits for them. Each selected commit is retained under `refs/commons-cache/commits/<SHA>` so Git maintenance cannot prune objects during a read, and `pins/<SHA>` beside the mirror records when a read last used it. A commit this process already pinned is read without any preparatory Git process; any other commit is checked, fetched when missing and pinned under the exclusive lock first. A tree reads after releasing the lock, protected by its pin.

`Cache.Maintain(ctx, git.MaintainOptions{PinTTL: ...})` deletes the pins no read used for `PinTTL` and runs `git maintenance run --task=gc` in every mirror. Choose a TTL longer than any tree stays open.

Reads share a limit of four concurrent readers per mirror within the process. Waiting for a reader slot honors context cancellation; cancelling a waiting caller does not interrupt active readers. This limit does not coordinate separate processes.

Run the focused integration specs against temporary local repositories:

```sh
go test ./git -run TestGit -ginkgo.focus='Cache.Blame|Cache.Changes|Cache.History|Cache.Maintain|Cache.ReadBlobs|cached immutable reads|read preparation|native Git tree' -ginkgo.no-color
```
