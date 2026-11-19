# Distributed leases

Import `github.com/flanksource/commons-db/lease` to coordinate callers that share a backend and application scope. Keep the backend client open for the lifetime of its leases.

| Constructor | Client | Coordination boundary |
| --- | --- | --- |
| `NewSQLServer(db, Options{Scope: "deploy"})` | `*sql.DB` using the SQL Server driver | The current database and application scope |
| `NewRedis(client, ExpiringOptions{Options: Options{Scope: "deploy"}})` | `valkey.Client` connected to a Redis or Valkey primary | The primary's database and application scope |
| `NewKubernetes(client, ExpiringOptions{Options: Options{Scope: "deploy"}})` | A namespaced `CoordinationV1().Leases(namespace)` client | The cluster, namespace, and application scope |
| `NewFile(dir, Options{Scope: "deploy"})` | A directory on a local filesystem | The processes on one host that share `dir`, and the application scope |

## Names and holders

`Options.Scope`, every key and every group must match `^[A-Za-z0-9_-]+$` and be at most 100 characters. A constructor refuses an invalid scope, and an acquisition or `Holders` call refuses an invalid key or group. Each error names the field and the value. Backends store the names as readable text:

| Backend | Key or resource for scope `deploy`, key `git-oma` |
| --- | --- |
| Redis and Valkey | `commons-db:lease:{deploy:git-oma}:writer`, `…:shared`, `…:waiting`, `…:holders` |
| SQL Server | App lock `deploy:git-oma`, plus `deploy:git-oma:gate` and, for group holders, `deploy:git-oma:group:1` through `:256` |
| Kubernetes | Lease `deploy-git-oma-<first 10 hex of sha256(scope, key)>`, annotated `commons-db.flanksource.com/resource: deploy:git-oma` |

Every acquisition names its caller in `LockOptions{Holder: "..."}`. A holder is required and must be printable UTF-8 of at most 128 bytes. `Lock.Holders(ctx)` lists the current holders of a key as `Holder{Name, Mode, Group, Since}`, where `Mode` is `write`, `read` or `group`. Call it after a failed `TryLock` to report who holds the key. The list is diagnostic and can be stale by the time it returns. Waiting writers are not holders.

## Lock modes

- `Mutex("git-azure-oma", lease.LockOptions{Holder: "ci-42"})` admits one holder. The same call can claim a leadership term.
- `ReadWriteLock(key, options)` allows concurrent readers or a single writer. A registered writer blocks new readers and group holders until the writer acquires or leaves. Writers and mutex holders on the same key exclude each other.
- `GroupLock("country", "USA", options)` can be held by several USA callers. A try-lock for `GroupLock("country", "RSA", options)` reports contention until every USA caller releases. Ungrouped readers on `country` can coexist with USA holders.

Use `Lock(ctx)` to wait, `TryLock(ctx)` to make one admission attempt, or `LockWait(ctx)` to receive the acquired lease asynchronously. `TryLock` returns a false acquisition flag and a nil error on contention. Use a cancellable context for channel waits and cancel it if you stop receiving. The channel can close without a result if the caller cancels before handoff. Receiving a successful result transfers the lease to the caller. Read acquisition uses `RLock`, `TryRLock`, or `RLockWait`.

The first example uses the lease for advisory coordination and cancels work when ownership can no longer be confirmed. A holder paused beyond the TTL may resume and write after a replacement acquires the lease. For correctness-critical writes, the protected storage must reject stale fencing tokens or use an equivalent guard that prevents former holders from writing.

```go
// updateRepo must return when its context is canceled.
func update(ctx context.Context, locks lease.Locker, updateRepo func(context.Context) error) (err error) {
    lock := locks.Mutex("git-azure-oma", lease.LockOptions{Holder: "ci-42"})
    held, acquired, err := lock.TryLock(ctx)
    if err != nil {
        return err
    }
    if !acquired {
        holders, err := lock.Holders(ctx)
        return errors.Join(fmt.Errorf("git-azure-oma is held by %v", holders), err)
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
options := lease.LockOptions{Holder: "ci-42"}
usa, err := locks.GroupLock("country", "USA", options).Lock(ctx)
if err != nil {
    return err
}
rsa, acquired, tryErr := locks.GroupLock("country", "RSA", options).TryLock(ctx)
if acquired {
    // USA ownership may have been lost before the try-lock.
    tryErr = errors.Join(rsa.Unlock(ctx), usa.Err())
}
return errors.Join(tryErr, usa.Unlock(ctx))
```

## Backend setup

SQL Server leases last as long as their session. Size a dedicated connection pool for the number of holders and waiting writers so lock sessions cannot starve the pool used by protected work. Grouped locks use 257 app locks per holder and require no schema objects. SQL Server ownership probes run every 10 seconds. Each lease pins its own session, sets the session's `CONTEXT_INFO` to the holder name before taking the app lock, and ends the session on unlock. A reader turned away at the writer gate holds no app lock yet, so it clears `CONTEXT_INFO` with `SET CONTEXT_INFO 0x` and returns its session to the pool; a session it cannot clear is ended instead. `Holders` reads `sys.dm_tran_locks` joined to `sys.dm_exec_sessions.context_info`, which needs `GRANT VIEW SERVER STATE TO [<login>]`; without it `Holders` returns an error naming that grant. SQL Server reports `Since` as the holder session's login time and cannot name a group holder's group, because only the group's digest reaches the server. `sys.dm_tran_locks` shows only the first 32 characters of an app-lock resource, so two keys whose `<scope>:<key>` share their first 32 characters can report each other's holders.

Redis and Valkey use a single authoritative primary per key. Asynchronous failover, data loss, or key eviction can allow overlapping holders. Use a primary with adequate memory and a `noeviction` policy. Writer acquisition uses a local copy of Redsync. Include the [Redsync license](internal/redsync/LICENSE) in distribution materials for binaries containing it. Holder names live in the `…:holders` hash, keyed by ownership token, and change in the same script call as the ownership they describe. The hash expires with its longest-lived holder, and `Holders` skips tokens that are no longer live.

Kubernetes requires `get`, `create`, and `update` on `coordination.k8s.io` Leases. The adapter retains empty Lease objects for reuse. An observer can need a full TTL from its first sight of a heartbeat to declare a crashed holder stale. Each locker retains this evidence in memory while the holder remains in the Lease object as last observed. Holder names sit beside each token in the `commons-db.flanksource.com/holders-v1` annotation. A new holder is admitted only while the annotations still fit Kubernetes' size limit with every holder's heartbeat and since at their widest encodings, so renewals never overflow it. A Lease whose `commons-db.flanksource.com/resource` annotation names a different scope and key is an error.

The file backend holds `flock` locks on `<dir>/<digest>.lock`, where the digest covers the scope and key. A mutex or writer takes it exclusively and a reader takes it shared. A waiting writer also holds `<dir>/<digest>.intent` exclusively, which turns new readers away until it acquires or leaves. The operating system releases a lock when its process exits, so a file lease never expires and is never lost. `GroupLock` returns `group locks are not supported by the file backend`. Use the file backend only on a local filesystem: `flock` over NFS detects a dead holder only when the NFS lease expires, and gives no loss signal. The lock files stay in `dir` after release.

The default expiry is 30 seconds. Redis/Valkey and Kubernetes attempt renewal every third of the TTL and signal loss when ownership cannot be confirmed. A paused process may miss the notification deadline. Expiry permits another caller to acquire, and `Done()` is a cooperative notification. Both leave fencing to the protected storage. The acquisition context bounds acquisition. After loss or normal completion, stop guarded work and call `Unlock` to release any remaining ownership.

## Integration tests

Run `go test -race ./lease/...` with any of `COMMONS_DB_LEASE_SQLSERVER_URL`, `COMMONS_DB_LEASE_REDIS_URL`, `COMMONS_DB_LEASE_VALKEY_URL`, or `COMMONS_DB_LEASE_KUBECONFIG` set for a disposable service. The Kubernetes integration suite creates temporary namespaces.
