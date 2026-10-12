---
title: Trace plugins
description: Write a trace kind — its params form, its record schema and its capture — and run it as a managed session that commits to a record store.
---

`github.com/flanksource/commons-db/tracing/traces` runs **trace plugins**. Each kind of capture declares three things:

- a **params form**: the JSON schema a client renders to start it;
- a **record schema**: the columns and filters its records are stored and read under;
- a **capture**: a handler that emits records until it is stopped, or until its source runs out.

The runtime runs each capture as a managed session of a `query.SessionRegistry`. A `recordstore/probe` commits the records into a stream of a `recordresults` store. That gives every kind:
- the session API's stop, extend, restart and events;
- one writer per stream;
- reads through the `traces/<kind>` result profile, with filtering, paging, follow and export.

## Writing a kind

A kind implements `TraceHandler[P, R]` over its params type `P` and its record type `R`:

```go
type HeartbeatParams struct {
	Every string `json:"every,omitempty" clicky:"title=Every"`
}

type Heartbeat struct {
	At   time.Time `json:"at" pretty:"label=At"`
	Beat int       `json:"beat"`
}

type heartbeats struct{}

// Params are the defaults a session starts from; P is reflected into the form.
func (heartbeats) Params() HeartbeatParams { return HeartbeatParams{Every: "1s"} }

// Schema declares the title, time and key columns; R's columns are reflected
// from its pretty/filter/sort tags.
func (heartbeats) Schema() recordresults.ResultType[Heartbeat] {
	return recordresults.ResultType[Heartbeat]{Title: "Heartbeats", TimeColumn: "at"}
}

// Handle emits until ctx is done. store reads back what it already committed.
func (heartbeats) Handle(ctx dbcontext.Context, p HeartbeatParams,
	records traces.Emitter[Heartbeat], store traces.Records[Heartbeat]) error {
	every, err := time.ParseDuration(p.Every)
	if err != nil {
		return err
	}
	for beat := 1; ; beat++ {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(every):
		}
		if err := records.Emit(ctx, Heartbeat{At: time.Now().UTC(), Beat: beat}); err != nil {
			return err
		}
	}
}
```

How the handler's params and records behave:

- **Params validation.** Params are decoded over `Params()`'s defaults, and unknown fields are refused. Params that implement `Validate() error` are checked too. All of this happens before any session exists.
- **`Emit`** waits while the session's buffer is full, so a slow store holds the capture back rather than growing memory. The buffer is full at `Runtime.BufferRows` records (10,000) or `Runtime.BufferBytes` of their keys and text (64MiB), whichever comes first; one record larger than that is still taken into an empty buffer.
- **`TryEmit`** never waits. A record it cannot buffer is dropped, counted in the session summary, and reported as a warning. Use it from a callback that runs on someone else's goroutine, such as a request's.
- **Handle's return.** Returning nil ends the capture normally. An error fails the session, but the records already emitted are still committed and sealed.

### Reporting beyond records

A capture can say more about itself than its records, through the same `Emitter`:

| Method | Effect |
| --- | --- |
| `Report(summary)` | Replaces the kind's own summary, such as the events its source lost. The session shows the latest one under `summary.source.report` while it runs and keeps the last. |
| `Warn(message)` | Adds a warning the session ends with, after the runtime's own (dropped or unencodable records). |
| `Describe(handle, metadata...)` | Names the session by what the capture set up on its source, such as an XE session, with the statements that set it up. A session records these once, as it starts running, so call it from `Prepare`. |

A kind that keeps data apart from its records, such as HTTP bodies served only on request, writes it to a companion stream named after its own: `traces.StreamOf(ctx)` is that stream, under the context `Prepare` and `Handle` run with.

### Setup that must finish before the session runs

A kind that subscribes to something, or creates a server-side session, implements `Preparer[P, R]`:

```go
Prepare(ctx dbcontext.Context, params P, records Emitter[R]) (prepared dbcontext.Context, release func() error, err error)
```

- `Prepare` runs synchronously as the session starts, before it reports running. A subscription made there misses nothing, and an error refuses the start.
- `Handle` then runs under the context `Prepare` returned, which can carry what it set up.
- `release` runs once `Handle` has returned, or at once if the capture never started. An error it returns, such as a server-side session that could not be dropped, becomes the session's error; the records are still committed and sealed.

### Readings at a host's checkpoints

A kind that reads state rather than observing events, such as an expression evaluated against a JVM, can take a reading at each checkpoint its host marks by implementing `Checkpointer[P, R]`:

```go
Checkpoint(ctx dbcontext.Context, params P, records Emitter[R]) error
```

A host marks a checkpoint with `Runtime.Checkpoint(ctx, session)`, for example at the end of each step a span covers. The kind's reading is taken, then the session is sampled, so the reading is committed when `Checkpoint` returns. The session's background polls are not checkpoints. For a kind that takes no readings, `Checkpoint` only samples the session.

### Defaults a host gives

`Handler.WithDefaults(params)` starts a kind's form and its captures from `params` rather than the handler's own `Params()`. A host that knows its connections prefills them this way, such as `sqlstatements.Kind().WithDefaults(sqlstatements.Params{Connections: []string{"self"}})`.

### Capabilities

`NewHandler(handler, Capabilities{...})` says how the kind captures:

| Capability | Meaning |
| --- | --- |
| `Live` | It captures what happens while the session runs. |
| `Historical` | It reads what already happened, and `Handle` returns by itself; the session then completes. |
| `Follow` | It goes on to read what happens next. |

## Processing records before they are stored

Options on the `Handler` process each record in this fixed order, after deduplication and before buffering. Nothing reaches the store, or a spool, unprocessed.

| Option | Effect |
| --- | --- |
| `WithDeduplication(key, window)` | Drops a record whose key the session already emitted within the window its params give. It is off unless configured, and a window of zero keeps everything. |
| `WithTruncation(maxBytes)` | Cuts every text or JSON string longer than `maxBytes` and lists the cut paths in a `truncated` column. The record must embed `traces.Truncation`. |
| `WithJSONProcessor(paths...)` | Parses the text fields at `paths` (dotted JSON names such as `response.content.text`) when they hold a JSON object or array, so they are stored, masked and filtered as structure. Each path must name a string field of the record, or the kind is refused. `Records.Scan` hands that JSON back as text, re-encoded, so key order and spacing can differ from what was emitted. |
| `WithSecretMasking(keep...)` | Masks sensitive keys and HAR name/value pairs, and strips secrets from every other string. Keys named in `keep` are never masked. |

Two more ways a kind can avoid duplicates:
- **Key column.** A kind with a `KeyColumn` stores each key once per stream. The store skips a repeated key, and a page never names one key twice: the session summary's `collapsed` counts the copies a page left out.
- **Retries.** A retried append is idempotent through the probe's cursor, separately from content deduplication.

## Serving kinds

```go
kinds := traces.NewKinds()
kinds.RegisterKind("heartbeat", traces.NewHandler(heartbeats{}, traces.Capabilities{Live: true}))

results, _ := recordresults.Open(recordresults.OpenOptions{
	Prefix: "traces", ConnectionName: "traces", Settings: settings,
	Register: kinds.RegisterResultTypes, // declares every kind's result type
})
runtime := &traces.Runtime{Kinds: kinds, Results: results, Probes: probe.NewManager(probe.ManagerOptions{LockDir: lockDir})}
runtime.Sessions = query.NewSessionRegistry(query.RegistryOptions{
	BeforeRead: results.Registry.BeforeRead,
	Restarters: map[string]query.RestartFunc{traces.ProfilePrefix: runtime.Restarter(baseContext)},
})

managed, err := runtime.Start(ctx, traces.StartRequest{Kind: "heartbeat", Params: json.RawMessage(`{"every": "5s"}`)})
```

- **Errors.** `Start` refuses an unknown kind with `ErrUnknownKind` and params the kind refuses with `ErrInvalidParams`.
- **Profile.** Each session runs as profile `traces/<kind>`, which keys its authorization and its restart.
- **Records.** `SessionStatus.Events` names the stream; read it through the `traces/<kind>` profile with `stream` set to it.

`Kinds.List()` describes each kind for a client: its params schema with defaults, its columns and its capabilities.

## The query server

`query serve` serves these kinds:

| Kind | Records |
| --- | --- |
| `http` | Outbound HTTP exchanges of chosen features (`prometheus`, `loki`, `http`, ...), one HAR entry each. |
| `sql` | SQL statements commons-db runs on the connections it names. `"*"` names every connection and `"self"` the server's own database; `"*"` does not include `"self"`, and naming none is refused. |
| `sql_xevent` | SQL Server Extended Events, as `sqltrace` event rows, deadlock reports decoded (`sqltrace.DecodeEvent`). The session is named after its XE session, reports an `xetrace.Summary` and warns of events it lost. A host with its own params form serves it with `xevent.NewKindFor`, over a params type whose `XEventOptions()` names the connection, the XE session and its drain. |
| `opensearch` | Span documents of an opentelemetry connection's index, over a window and optionally followed. |

Its routes:
- `GET /api/v1/traces/kinds` lists the kinds.
- `POST /api/v1/traces/{kind}/sessions` starts a session, with body `{"params": {...}, "duration": "5m"}`.
- `/api/v1/sessions/{id}/...` stops, extends, restarts and streams it.

`/api/openapi.json` describes one start operation per kind, whose request body is that kind's params form.

The routes authorize nothing themselves. A host that serves them sets `sessions.Options.Authorize`, which is asked for every start, read and control of a session under its `traces/<kind>` profile; without it, as in `query serve`, anyone who reaches the server can start a capture. A host such as oipa authorizes its users in front of these routes.

A refused start is answered with its error as text: 404 for an unknown kind, 409 at the session limit, 400 otherwise. A host that can say more about a refusal sets `sessions.Options.TraceStartError`. It is given the error first, and when it returns true its own response stands. oipa uses it to answer a missing SQL Server grant with a 403 that carries the grant statements.

The `http` and `sql` kinds observe commons-db through taps in the `connection` package:
- `ObserveHTTP` hands a HAR collector every exchange of a feature, whatever its HAR level.
- `ObserveSQL` hands an observer every statement published for a connection, for every connection (`EveryConnection`), or for the server's own database (`OwnDatabase`).

Both are process-wide: a session sees every caller's traffic for what it observes.
