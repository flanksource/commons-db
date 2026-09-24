---
title: Serving over HTTP
description: Mount result profiles in the profile service, resolve the index connection, catch the index up before reads, and serve follow sessions.
---

The `Registry` connects to the profile engine at three points. A host serving result profiles wires all three:

| Point | Registry method | Purpose |
| --- | --- | --- |
| profile store | the `Registry` itself (a `profiles.VirtualStore`) | lists and gets the read-only result profiles. Layer it over your own store with `profiles.NewOverlayStore`. |
| connection resolver | `registry.ResolveConnection` | resolves `connection://<prefix>/<name>` (and the connection's id) to the index's read-only SQLite DSN. Any other reference returns "not mine", so resolution carries on. |
| before-read hook | `registry.BeforeExecute` / `registry.BeforeRead` | catches the index up for each requested stream, checks it exists and holds the right kind, and holds an index lease across the read |

## Page reads

```go
base, err := profiles.NewFileStore(profileDir)
store, err := profiles.NewOverlayStore(base, results.Registry)
queryCtx := dbcontext.New().WithConnectionResolver(results.Registry.ResolveConnection)

service, err := profiles.New(profiles.Options{
	Store:         func() (profiles.Store, error) { return store, nil },
	Context:       func() dbcontext.Context { return queryCtx },
	DecodeBody:    profiles.DecodeRequestBody,
	BeforeExecute: results.Registry.BeforeExecute,
})
service.RegisterFamily()

root := &cobra.Command{Use: "acme"}
rpcServer := rpc.NewSwaggerServer(
	&rpc.ServeConfig{
		Title: "acme", Version: version, SkipHealth: true,
		Executor: &rpc.ExecutorConfig{Enabled: true, SkipPreRun: true, PathPrefix: "/api/v1"},
	},
	root, &rpc.OpenAPIConfig{Title: "acme", Version: version},
)
mux := http.NewServeMux()
rpcServer.RegisterRoutes(route.NewRouter(mux))

handler, err := service.Handler("/api/v1", mux)
```

`service.Handler` serves `GET`/`HEAD`/`POST /api/v1/profile/<name>` itself and passes everything else to `next`. With clicky's `rpc` server as `next`, the profile family (`RegisterFamily`) also serves each profile's schema (`Accept: application/json+clicky`), filter-value lookups (`?__lookup=filters`) and OpenAPI. A profile is reachable under its escaped name (`/api/v1/profile/traces%2Fquery_event`) and under the family entity path (`/api/v1/profile/profile-traces-query-event`).

### What BeforeExecute guarantees

For every result profile in a read batch (other profiles pass through untouched):

1. The request must carry a valid `stream` param. Otherwise the error is `profiles.ErrProfileRequestInvalid`.
2. `Indexer.Ensure` catches the index up with the source for that stream.
3. It takes one **index lease** across the whole batch, then re-validates under the lease. That closes the gap where the sweeper could remove a stream after `Ensure` but before the query starts.
4. A stream that doesn't exist, or holds another kind, is `profiles.ErrProfileDataNotFound`, never an empty page. An empty page would claim "nothing matched" about a stream nobody wrote.

The returned release function drops the lease. The profile service calls it when the read finishes.

## Follow sessions

A result type with `Follow: true` can be tailed through the sessions API. Mount `sessions` in front of the profile service, over the same store and query context, and give the session registry `BeforeRead`:

```go
sessionRegistry := query.NewSessionRegistry(query.RegistryOptions{
	BeforeRead: results.Registry.BeforeRead,
})
defer sessionRegistry.StopAll()

sessionService, err := sessions.New(sessions.Options{
	Profiles: func() (profiles.Store, error) { return store, nil },
	Context:  func() dbcontext.Context { return queryCtx },
	Registry: sessionRegistry,
	Records:  results.Backend, // replays and follows recorded events after the session leaves the registry
})
handler, err := sessionService.Handler("/api/v1", profileHandler) // profileHandler = service.Handler(...) above
```

`BeforeRead` runs the same catch-up and checks as `BeforeExecute`. A top session keeps the lease for its sample. A trace (follow) gives the lease back at once, because it reads for as long as it lasts and holding a lease that long would block every append. The follow provider takes its own short lease around each read.

Starting and reading a follow:

```bash
# start: returns the session info, including its id
curl -X POST "http://localhost:8080/api/v1/profile/profile-traces-query-event/sessions?follow=true&stream=job-42&afterSeq=0&filter.user=!batch"

# events as server-sent events; reconnect with Last-Event-ID to resume
curl -N "http://localhost:8080/api/v1/sessions/$ID/events"

# status
curl "http://localhost:8080/api/v1/sessions/$ID"
```

Only followable types get a `/sessions` path in OpenAPI. Starting a follow on another type is refused.

## Tenancy

When the `Source` is a `recordstore.Router`, the route comes from the request context. Your auth middleware must put the tenant on `r.Context()` before the profile and sessions handlers run, and `RouterOptions.Route` must read it back:

```go
http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFromAuth(r)
	handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tenantKey{}, tenant)))
})
```

Streams written for one tenant are `ErrNotFound`, and so `ErrProfileDataNotFound`, for every other tenant. Use `sessions.Options.Authorize` and `Principal` to control who may start, stop or read a session.
