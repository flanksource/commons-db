# Git read cache

`readcache.Cache` deduplicates concurrent lookups and stores successful byte results through an injected `Store`. `git.WithReadCache(ctx, cache)` selects an adapter for one operation; `git.ClientOptions.ReadCache` selects one for repositories created by that client. Pass the active request context to every read.

```go
reads := &readcache.Cache{Store: readcache.Filesystem{Root: ".cache/git/reads"}}
ctx = git.WithReadCache(ctx, reads)
ctx = git.WithSecurityNamespace(ctx, "tenant-one")
files, err := (&git.Cache{Root: ".cache/git"}).Files(ctx, repository, fullCommitSHA)
```

`Store.Get`, `Store.Set`, and `Store.Delete` receive the caller's context. `Get` distinguishes a missing value from a backend failure. `Set` receives the retention duration. Backend errors are returned to the caller; the cache does not switch adapters. Implement these three methods to supply another backend. `Memory` bounds its entries and expires values; `Filesystem` writes atomically and removes expired entries when accessed. Neither adapter stores credentials in filenames.

Git read keys hash the provider, repository identity, authentication scope, security namespace, operation, complete options, and a schema version. Pin revisions to full 40-character SHAs to reuse immutable reads for 24 hours. Remote refs expire after 30 seconds; use `git.WithFreshRefs(ctx)` when the operation requires a new listing. Relative history dates bypass immutable caching. Errors and canceled computations are never stored. Reusing a stored result returns independent bytes and does not run Git or contact a provider.

One caller owns each in-flight computation. A canceled waiter stops waiting without canceling that owner. If the owner is canceled, remaining waiters retry with their own context. The cache retains serialized values, not request contexts, open trees, leases, tasks, or streams. Close live tree handles normally.

Use `progress.WithTasks(ctx, progress.TaskOptions{})` to expose Git fetch and read-cache progress in the Clicky group carried by the context. Hosts can supply `TaskOptions.Group` when the context has no active group; nested work stays in its active child group. Fetch steps run inline and consume no extra worker slot. Cache events start before adapter IO and distinguish a load, a stored hit, and a joined lookup. `readcache.WithMetadata(ctx, readcache.Metadata{Operation: "files", Repository: repository.URL.Redacted(), Ref: fullCommitSHA})` supplies display metadata without exposing the opaque cache key or credentials.

Task names start with `[owner/repository]` (Azure DevOps uses `[project/repository]`) and carry separate repository, ref, and operation labels. Fetch descriptions contain only the current phase; Clicky renders numeric counts, units, bytes and throughput from task progress fields. Git's terminal meter is decoded internally and is never copied into descriptions or completion logs. `progress.WithTasks` also observes `cat-file` reads: batches report their request count and measured content bytes, while open tree readers leave the total unknown until closed. Missing and empty blobs complete a request without adding content bytes. Cache hits do not manufacture fetch or object-read activity.

With a request timing collector, `git_cache` measures the entire lookup, including adapter IO, shared-load waits, and writes. Its counters are `ops`, `hit`, `load`, `joined`, and `errors`. Read-session memo counters in `release_reuse` distinguish completed successful `*_hits` from in-flight `*_joined` reads; `git_<operation>_wait` records shared-load waiting time. Native Git commands, including patch and discovery operations, contribute once to `git` and their `git_<subcommand>` metrics.

Run the cache regressions with `go test -race ./git/readcache ./git/progress`; run repository integration tests with `go test -race ./git/...`.
