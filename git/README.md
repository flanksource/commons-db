# Git repository caches

`Cache{Root: ...}` stores fetched Git objects and leases working trees. Repositories opened with Azure DevOps, GitHub, or GitLab provider clients automatically probe existing complete mirrors before creating a new store. A confirmed common ancestor allows the new repository to reuse that store. Plain Git connections retain per-repository caching.

```go
cache := &git.Cache{Root: cacheDirectory}
worktree, err := cache.Checkout(ctx, repository, git.CheckoutOptions{
    Ref: "main", Ephemeral: true,
})
if err != nil {
    return err
}
defer worktree.Release()
```

`Root` is the sharing boundary and must be a trusted local cache directory. The supplied repository provides its own credentials and hosting API. Discovery uses metadata-only REST requests; it never requires Git protocol v2. Unknown candidate commits are tried against cached roots. Authentication, rate limits, invalid responses, and network failures are returned to the caller.

Each logical repository records its physical owner in an atomic, versioned `repository.json`. Existing mirrors are adopted in place. Existing stores are never moved, merged, or deleted by discovery. Removing an owner directory while bindings reference it produces an explicit error. Maintenance visits physical mirrors once; active worktrees remain registered with Git and retain their commits.

Fetched refs, writable branches, and tags are isolated by repository. Worktree-created refs also include the lease identity. Push operations retain the requested remote branch/tag names, and annotated tags retain their logical tag names.

## Independent clones

Pass the same cache to `CloneOptions` to fetch once into the cache and copy the requested history locally:

```go
_, err := git.Clone(ctx, repository, git.CloneOptions{
    Dir: destination, Ref: "main", Cache: cache,
})
if err != nil {
    return err
}
```

The destination has its own objects, its `origin` names the requested repository, and it remains usable after the cache is removed. No persistent alternates are installed. Omitting `Cache` uses the ordinary remote clone path.

`Depth` limits standalone clone history. Shallow cache-assisted clones and provider-backed shallow checkouts use isolated depth-specific stores so they cannot truncate a shared complete mirror. Discovery timing is recorded in the existing Git timing collector; pack transfer bytes remain available through fetch progress observations.
