---
title: Consuming commons-db
description: Add commons-db's modules to another Go application, carry the sqlite replace, and develop against a local checkout.
---

commons-db ships as **four Go modules** from one repository: the root module for connections and the profile engine, the `recordstore` module for record streams and the result types served over them, the `tracing/sqltrace` module for SQL Server trace capture into a record stream, and the `cmd/query` module for the profile HTTP service and the `query` CLI.

| Module | Tag format | Brings |
| --- | --- | --- |
| `github.com/flanksource/commons-db` | `v0.1.45` | `sqlite`, `db/sqlitetable`, `query` (the profile engine), `query/profilestore`, `tracing` (xetrace, deadlocks), connections, migrations |
| `github.com/flanksource/commons-db/recordstore` | `recordstore/v0.1.45` | `recordstore` and its backends, probes, `recordresults` |
| `github.com/flanksource/commons-db/tracing/sqltrace` | `tracing/sqltrace/v0.1.45` | `sqltrace` (SQL Server Extended Events capture, committing to a recordstore backend) |
| `github.com/flanksource/commons-db/cmd/query` | `cmd/query/v0.1.45` | `profiles` (the profile HTTP service), `sessions` (trace/top sessions), the `query` CLI and its UI |

The modules are released together, so pin every one you use to the **same version**:

```bash
go get github.com/flanksource/commons-db@v0.1.45
go get github.com/flanksource/commons-db/recordstore@v0.1.45
go get github.com/flanksource/commons-db/tracing/sqltrace@v0.1.45
go get github.com/flanksource/commons-db/cmd/query@v0.1.45
```

A store-only consumer, such as a CLI that writes streams another process serves, needs only the `recordstore` module, which brings the root module with it.

A trace-capturing consumer needs the `tracing/sqltrace` module, which brings the `recordstore` and root modules with it. `recordstore` does not depend on `tracing/sqltrace`.

Never mix the `recordstore` or `tracing/sqltrace` module with a root module at `v0.1.44` or older. Those root releases still contain `recordstore/` and `tracing/sqltrace/` themselves, so every import of them becomes ambiguous.

## Required: the sqlite driver replace

commons-db links exactly one `database/sql` driver named `sqlite`: the pure-Go `modernc.org/sqlite`. The gorm dialector `github.com/glebarez/sqlite` links a second copy of the engine (`github.com/glebarez/go-sqlite`), which registers `sqlite` again. A binary that links both panics at startup:

```text
panic: sql: Register called twice for driver sqlite
```

commons-db's own `go.mod` replaces the dialector with a fork that reuses modernc. **`replace` directives are not inherited by consumers**, so every application module that depends on commons-db must carry the same line in its own `go.mod`:

```go title="go.mod"
// github.com/glebarez/sqlite links its own copy of the sqlite engine and
// registers the "sqlite" driver a second time. The clarkmcc fork imports
// modernc.org/sqlite/lib, so the driver is registered exactly once.
replace github.com/glebarez/sqlite => github.com/clarkmcc/gorm-sqlite v0.0.0-20240426202654-00ed082c0311
```

Never import `github.com/glebarez/go-sqlite` directly. If the panic shows up anyway, run `go mod why github.com/glebarez/go-sqlite` to find the path that pulls it in.

## Linking the profile providers

Result profiles read their index through the `sqlite` query provider, which registers itself in `init()` in `github.com/flanksource/commons-db/query/providers`. `cmd/query/profiles` links it, so a binary serving profiles through that package has it. `recordresults` deliberately does not, so a binary that only captures streams never links every provider's SDK. If you build a binary that reads profiles without importing `profiles`, add the blank import yourself:

```go
import _ "github.com/flanksource/commons-db/query/providers"
```

Without it, a followable result type fails with `recordstore provider: … link github.com/flanksource/commons-db/query/providers`.

## Configuration through properties

Store settings are read from `github.com/flanksource/commons/properties`, which covers `-P key=value` flags, the environment and a properties file, under a prefix your application picks. Each store gets its own prefix, so one binary can run several:

```properties
trace.store.backend=sqlite
trace.store.dir=/var/lib/acme/traces
trace.store.ttl=30d
trace.store.ndjson.maxBytes=256MiB
trace.store.ndjson.keepStreams=20
```

See [Settings and routing](../../recordstore/settings/) for how an unset backend gets resolved.

## Developing against a local checkout

Use a Go workspace next to your application to build against an unreleased commons-db. Do not commit a `replace` that points at a local path.

```bash
# in your application's parent directory
go work init ./acme-app
go work use ../commons-db ../commons-db/recordstore ../commons-db/tracing/sqltrace ../commons-db/cmd/query
```

`go.work` and `go.work.sum` are ignored by commons-db's own `.gitignore`. Keep them out of your application's repository too. When the change is done, fix shared behaviour in commons-db, release it, and bump every commons-db module in your application together.

## Tests that need a database

commons-db's Postgres-backed packages resolve their test database through `dbtest`. None of the packages on this site need Postgres: `recordstore`, `sqlite` and `recordresults` run on temporary SQLite files and an in-process kv store (`cache.NewMemory()` from `github.com/flanksource/clicky/cache`). Their tests use Ginkgo. The ones that exercise valkey start `miniredis` in-process.

## Checklist

- [ ] `github.com/flanksource/commons-db`, `…/recordstore` and `…/cmd/query` pinned to the same version
- [ ] `replace github.com/glebarez/sqlite => github.com/clarkmcc/gorm-sqlite …` in your `go.mod`
- [ ] no committed `replace` pointing at a local path
- [ ] one property prefix per record store, such as `trace.store`
- [ ] a writable directory for SQLite and NDJSON files that persists across restarts when streams must survive them
