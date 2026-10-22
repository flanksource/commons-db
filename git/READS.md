# Immutable repository reads

`Cache` uses its existing bare mirror for history, commit counts, tree changes, selected files and supplied-content blame. These reads require Git 2.42 or newer. They do not lease worktrees.

Resolve a branch or tag to a full commit SHA when the caller needs several operations to describe the same revision. An operation lists remote refs once for named references. Full SHAs use cached commits without remote discovery; missing commits are fetched. History and blame complete shallow ancestry when necessary. Tree changes and blob reads only need the selected endpoint objects.

## Compare first, load on selection

```go
changes, err := cache.Changes(ctx, repo, git.ChangesOptions{
    Base: baseSHA,
    Head: headSHA,
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

`Changes` compares two trees using native Git metadata and returns literal paths, old/new object IDs and modes. Renames appear as deletion and addition. It includes mode-only changes so consumers can decide whether those matter.

`ReadBlobs` accepts full 40-character commit SHAs and repository-relative file paths. Results preserve request order. `Present: false` means the path is absent at that commit; a present empty file has `Present: true` and zero content bytes. Directory paths, invalid commits, malformed batch output and command failures return errors. One `cat-file --batch -Z` process handles the batch, including filenames containing tabs or newlines. `Show` delegates to this reader and returns `ErrPathNotFound` for an absent file.

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

Close the tree when its consumer finishes. It owns a reader slot and one lazily started `cat-file --batch -Z` process. Directory traversal loads native tree metadata once; body reads use that process. Local repositories use private bare metadata and object alternates in the configured cache, removed on close; source files are never extracted. Symlinks are listed but cannot be read as ordinary files. Cancellation terminates the owned Git process and close releases its resources.

## Concurrency and retention

Preparation and fetching hold the existing mirror writer lock. Each selected commit is retained under `refs/commons-cache/commits/<SHA>` so Git maintenance cannot prune objects during a read. These refs live with the cache; this API does not expire them independently.

Reads release the writer lock before running Git and share a limit of four concurrent readers per mirror within the process. Waiting for a reader slot honors context cancellation; cancelling a waiting caller does not interrupt active readers. This limit does not coordinate separate processes.

Run the focused integration specs against temporary local repositories:

```sh
go test ./git -run TestGit -ginkgo.focus='Cache.Blame|Cache.Changes|Cache.History|Cache.ReadBlobs|cached immutable reads' -ginkgo.no-color
```
