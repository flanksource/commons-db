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

The cache accepts a known SHA from the repository mirror or its organization pool. The pool retains the commits of each repository by its key, with a stamp of when a cache last used it. Maintenance removes the refs of repositories unused for `PoolRetain` before collecting unreferenced objects. `Evict` operates on one repository key and refuses a mirror with a leased worktree.

## Shared object pools

By default the pools live under `Root/v2/pools`, locked with `flock` files there. To share them between hosts, for example on a ReadWriteMany volume, set `PoolRoot` to the pool volume and `Locker` to a `lease.Locker` with scope `git-pool` that every sharing host reaches:

```go
locks, err := lease.NewRedis(valkeyClient, lease.ExpiringOptions{Options: lease.Options{Scope: "git-pool"}})
if err != nil {
    return err
}
cache := &git.Cache{Root: localDisk, PoolRoot: "/mnt/git-pool", Locker: locks, PoolRetain: 30 * 24 * time.Hour}
```

- A cache with `PoolRoot` and no `Locker` fails at its first pool use with `shared git pool <path> requires Cache.Locker`. It never falls back to `flock`, which cannot detect a dead holder across hosts.
- Every host mounts the pool at the same path. Mirrors name it in an absolute alternate, and a mirror whose alternate points at a different pool is refused, naming both paths.
- Mirrors and worktrees stay local to each cache's `Root`, under `flock`.
- `Holder` names this process in lease reports. It defaults to `POD_NAME`, else `<hostname>-<pid>`.
- Pool maintenance prunes a repository only once no cache has pinned, fetched or reconciled it for `PoolRetain` (30 days by default). Every cache that shares a pool must run `Maintain` more often than that, because its reconcile step is what keeps the repositories its idle mirrors borrow from.

See [READS.md](READS.md#shared-pools) for the lease and retention details.

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

`Depth` limits standalone clone history. The cache itself holds complete histories only, stored once in the organization pool; see [READS.md](READS.md#complete-histories-stored-once). Fetch progress reports the pool key and transferred bytes when available.
