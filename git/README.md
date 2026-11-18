# Git repository caches

`Cache{Root: ...}` stores fetched Git objects and leases working trees. Each repository has its own bare mirror and worktrees under `Root/v2/<repository-key>`. Complete GitHub and Azure DevOps mirrors automatically use a ref-bearing alternate for their organization. Git negotiates reuse directly with the remote. Local repositories and other Git providers use per-repository mirrors.

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

The pool key comes from the provider instance and organization in the clone URL. GitHub uses the owner namespace. Azure DevOps Services recognizes its HTTPS and SSH aliases and shares across projects in the same organization. Azure DevOps Server uses the server and collection. Repositories in an organization share objects across connection credentials. Git uses the requested repository's credentials for network requests.

The cache accepts a known SHA from the repository mirror or its organization pool. Each mirror retains the pool commits its worktrees can use until the mirror is evicted. Maintenance removes retired borrowers' refs before collecting unreferenced objects. `Evict` operates on one repository key and refuses a mirror with a leased worktree.

Set `Cache.Store` to a `MirrorStore` or `PackStore` to hydrate the repository mirror from a durable tier. `Cache.Fetch` with `Publish: true` writes a self-contained entry by the repository key. A fresh local cache can restore a published SHA while the remote is offline. The built-in stores write under `Path/v2`. Older caches and store entries are available for rollback in their original directories.

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

`Depth` limits standalone clone history. Shallow clones and checkouts use depth-specific mirrors outside the organization pool. Fetch progress reports the pool key and transferred bytes when available.
