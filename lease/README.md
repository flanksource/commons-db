# Distributed leases

Import `github.com/flanksource/commons-db/lease` to coordinate callers that share a backend and application scope. Keep the backend client open for the lifetime of its leases.

| Constructor | Client | Coordination boundary |
| --- | --- | --- |
| `NewSQLServer(db, Options{Scope: "deploy"})` | `*sql.DB` using the SQL Server driver | The current database and application scope |
| `NewRedis(client, ExpiringOptions{Options: Options{Scope: "deploy"}})` | `valkey.Client` connected to a Redis or Valkey primary | The primary's database and application scope |
| `NewKubernetes(client, ExpiringOptions{Options: Options{Scope: "deploy"}})` | A namespaced `CoordinationV1().Leases(namespace)` client | The cluster, namespace, and application scope |

## Lock modes

- `Mutex("git/azure/oma")` admits one holder. The same call can claim a leadership term.
- `ReadWriteLock(key)` allows concurrent readers or a single writer. A registered writer blocks new readers and group holders until the writer acquires or leaves. Writers and mutex holders on the same key exclude each other.
- `GroupLock("country", "USA")` can be held by several USA callers. A try-lock for `GroupLock("country", "RSA")` reports contention until every USA caller releases. Ungrouped readers on `country` can coexist with USA holders.

Use `Lock(ctx)` to wait, `TryLock(ctx)` to make one admission attempt, or `LockWait(ctx)` to receive the acquired lease asynchronously. `TryLock` returns a false acquisition flag and a nil error on contention. Use a cancellable context for channel waits and cancel it if you stop receiving. The channel can close without a result if the caller cancels before handoff. Receiving a successful result transfers the lease to the caller. Read acquisition uses `RLock`, `TryRLock`, or `RLockWait`.

```go
// updateRepo must return when its context is canceled.
func update(ctx context.Context, locks lease.Locker, updateRepo func(context.Context) error) (err error) {
    held, err := locks.Mutex("git/azure/oma").Lock(ctx)
    if err != nil {
        return err
    }
    work, cancel := context.WithCancel(ctx)
    defer cancel()
    defer func() { err = errors.Join(err, held.Unlock(ctx), held.Err()) }()
    go func() {
        select {
        case <-held.Done():
            cancel()
        case <-work.Done():
        }
    }()
    return updateRepo(work)
}
```

```go
usa, err := locks.GroupLock("country", "USA").Lock(ctx)
if err != nil {
    return err
}
rsa, acquired, tryErr := locks.GroupLock("country", "RSA").TryLock(ctx)
if acquired {
    // USA ownership may have been lost before the try-lock.
    tryErr = errors.Join(rsa.Unlock(ctx), usa.Err())
}
return errors.Join(tryErr, usa.Unlock(ctx))
```

## Backend setup

SQL Server leases last as long as their session. Size a dedicated connection pool for the number of holders and waiting writers so lock sessions cannot starve the pool used by protected work. Grouped locks use 257 app locks per holder and require no schema objects. SQL Server ownership probes run every 10 seconds.

Redis and Valkey use a single authoritative primary per key. Asynchronous failover, data loss, or key eviction can allow overlapping holders. Use a primary with adequate memory and a `noeviction` policy. Writer leases use a local copy of Redsync. Include the [Redsync license](internal/redsync/LICENSE) in distribution materials for binaries containing it.

Kubernetes requires `get`, `create`, and `update` on `coordination.k8s.io` Leases. The adapter retains empty Lease objects for reuse. A new observer can need a full TTL to declare a crashed holder stale.

The default expiry is 30 seconds. Redis/Valkey and Kubernetes renew every third of the TTL and signal loss before expiry when ownership cannot be confirmed. The context used to acquire a lock bounds acquisition. Work cancellation is cooperative and callers are responsible for relinquishing a lease after guarded work stops.

## Integration tests

Run `go test -race ./lease/...` with any of `COMMONS_DB_LEASE_SQLSERVER_URL`, `COMMONS_DB_LEASE_REDIS_URL`, `COMMONS_DB_LEASE_VALKEY_URL`, or `COMMONS_DB_LEASE_KUBECONFIG` set for a disposable service. The Kubernetes integration suite creates temporary namespaces.
