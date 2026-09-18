---
title: Settings and routing
description: Read store settings from properties, resolve the backend per caller, and route calls to a backend per tenant.
---

## Settings

`recordstore.Settings` says where a store's streams live and for how long. `ReadSettings` reads them from `commons/properties` (`-P` flags, the environment, a properties file) under a prefix you choose, on top of your defaults:

| Property | Field | Value |
| --- | --- | --- |
| `<prefix>.backend` | `Backend` | `kv`, `sqlite` or `ndjson`. If unset, `Resolve` picks one. |
| `<prefix>.dir` | `Dir` | where sqlite files and ndjson streams live |
| `<prefix>.ttl` | `TTL` | how long a stream is kept: `30d`, `36h`, … |
| `<prefix>.ndjson.maxBytes` | `NDJSONMaxBytes` | one ndjson stream's cap: `256MiB`, … |
| `<prefix>.ndjson.keepStreams` | `NDJSONKeepStreams` | ndjson streams kept per kind |

```go
settings, err := recordstore.ReadSettings("trace.store", recordstore.Settings{
	Dir:               filepath.Join(home, ".acme", "traces"),
	TTL:               7 * 24 * time.Hour,
	NDJSONMaxBytes:    256 << 20,
	NDJSONKeepStreams: 20,
})
```

A key that is unset keeps its default. A key that is set but unusable returns an error naming the key, such as `trace.store.ttl must be a positive duration such as 30d or 36h, got "soon"`. It never silently falls back to the default.

## Resolving the backend

```go
func (s Settings) Resolve(hasKV bool, fallback BackendKind) (BackendKind, error)
```

`Resolve` returns the backend the store writes to:

| `<prefix>.backend` | caller has a kv store | result |
| --- | --- | --- |
| set | — | as asked, except… |
| `kv` | no | **error**: writing somewhere else would lose the one property kv was asked for |
| unset | yes | `kv`, the store every process can share |
| unset | no | `fallback`, which must be `sqlite` or `ndjson` |

`hasKV` is a fact about **one caller**, and it can differ per tenant or environment, so resolve per call, not once at startup. For example, a CLI can give an environment whose cache has a valkey L2 the kv backend, and one without it `records.sqlite`.

## Routing per tenant

`recordstore.Router` is a `Backend` that resolves the backend of the route named by the call's context (a tenant or an environment) on every call. It opens that backend on first use and keeps it, so the backend's per-stream append serialization holds across calls.

```go
router, err := recordstore.NewRouter(recordstore.RouterOptions{
	Route: func(ctx context.Context) (string, error) {
		tenant, _ := ctx.Value(tenantKey{}).(string)
		if tenant == "" {
			return "", errors.New("the request carries no tenant")
		}
		return tenant, nil
	},
	Open: func(ctx context.Context, tenant string) (recordstore.Backend, error) {
		return kv.New(kv.Options{
			Store: valkey.NewStore(clientFor(tenant)), Prefix: "records", Schema: schemas.Kind,
			TTL: 30 * 24 * time.Hour, MaxChunkBytes: 1 << 20,
		})
	},
})
```

- **Stream ids aren't qualified by route.** Routes are kept apart because each owns its backend, so a stream written on one route is `ErrNotFound` on every other.
- The backend `Open` returns must belong to that route alone. It must never be shared with another route, and never be the index a registry reads, or one route's streams become readable through another. `recordresults.Open` enforces the second rule by always mirroring a `Source` into a derived index of its own.
- `router.Resolve(ctx)` returns the route's backend, to find where a stream lives. `recordresults.Results.Ref` uses it to report a `StoreLocation`.
- `router.Forget(route, backend)` drops and closes a route's backend so the next call opens it again, for example when the store behind the route went away. It compares backends by identity, so an old owner releasing late can't evict its replacement.
- `router.Close()` closes every route's backend and refuses later calls.

Pass the router to `recordresults.Open` as `OpenOptions.Source`, together with the `Schemas` its backends resolve through:

```go
results, err := recordresults.Open(recordresults.OpenOptions{
	Prefix: "traces", ConnectionName: "index",
	Settings: settings, // Dir and TTL still apply to the derived index
	Source:   router,
	Schemas:  schemas,
	Register: registerTypes,
})
```

## Stream locks

`recordstore.StreamLocks` is the per-stream mutex set backends use to keep the single-writer contract inside one process. The zero value is ready to use, and unused locks are dropped, so the set doesn't grow with every stream ever written.

```go
unlock := locks.Lock(stream)   // blocks until the stream is free
defer unlock()

if unlock, ok := locks.TryLock(stream); ok { // for work that should skip a busy stream
	defer unlock()
}
```
