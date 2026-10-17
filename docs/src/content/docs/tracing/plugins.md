---
title: Trace plugins
description: Write a trace kind — its params form, its record schema and its capture — and run it as a managed session that commits to a record store.
---

`github.com/flanksource/commons-db/tracing` runs **trace plugins**. Each kind of capture declares three things:

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
	records tracing.Emitter[Heartbeat], store tracing.Records[Heartbeat]) error {
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
| `Export(name, value)` | Hands the processes the capture observes an environment variable, such as `HTTPS_PROXY` for a proxy it listens on. Only `Prepare` exports: a name that is not an environment variable name, or one exported twice, refuses the start, and an export once the capture runs is left out with a warning. The session records the pairs as `SessionStatus.Exports` (`exports` in the sessions API), set as soon as `Runtime.Start` returns, so a host reads `managed.Status().Exports` and merges them into the env of the children it starts. |

A kind that keeps data apart from its records, such as HTTP bodies served only on request, writes it to a companion stream named after its own: `tracing.StreamOf(ctx)` is that stream, under the context `Prepare` and `Handle` run with.

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

### Exports and summaries of a window

A kind can download its records in formats of its own by implementing `Exporter[R]`, and summarize them by implementing `Summarizer[R]`:

```go
Formats() []tracing.ExportFormat // {Name, Ext, MIME}
Export(format string, rows iter.Seq[R], w io.Writer) error
Summarize(rows iter.Seq[R]) (map[string]any, error)
```

- **Formats.** Every kind exports `ndjson` (`tracing.NDJSON`): its rows as stored, one JSON object per line. An `Exporter`'s formats follow it. `RegisterKind` refuses a format without a name, extension or media type, and a name offered twice, `ndjson` included.
- **Rows.** `rows` are the window's records as `Records.Scan` reads them back, parsed JSON as text again.
- **Summaries.** A summary is the map an assertion over the window reads, such as a gavel fixture's CEL. Its `count` is always the window's records, whatever `Summarize` returns; a kind that is no `Summarizer` is summarized as `{"count": n}`.

The `query serve` kinds that summarize, and the keys they return besides `count`:

| Kind | Summary |
| --- | --- |
| `http` | `entries`, `errors` (a 4xx or 5xx, or no response), sorted `hosts`, `methods{}` and `statuses{"200": n}` counts, and the first 200 `requests` with `method`, `url`, `host`, `path`, `status`, `duration_ms`, `error`, `mime`, `tunnelled`, `request_headers{}`, `response_headers{}` and `query{}` (first value of each name), `body`, `response_size`, `response_body` (empty when not text) and, for a whole `application/json` body, `response_json`. It also exports `har`: a HAR 1.2 file of the window's exchanges. |
| `http_proxy` | As `http`, plus `tunnelled`, `bytes_in` and `bytes_out`; exports `har`. |
| `sql_statements`, `pg_proxy` | `statements`, `errors`, `duration_ms`, `by_op{}`, sorted `tables`, and the first 200 `queries` with `sql`, `op`, `tables`, `rows`, `duration_ms` and `error` (`sqlstatements.WindowVars`). |
| `terminal` | `events`, `duration_ms`, `exit_code` (-1 when none was set), `width`, `height`, `final` (the settled screen), `snapshots`, `duplicates[{text, count}]`, `has_duplicates` and `truncated`; exports `cast` (asciinema v2). |
| `process_tree` | `live` and `exited` processes, and the highest `peak_cpu_percent` and `peak_rss_bytes` any reached. |

### Seq windows

A host that runs work under a running capture slices the records that work caused by seq, not by clock:

```go
before, err := runtime.Mark(ctx, managed) // Checkpoint, then the seq the stream committed through
// ... run the work ...
after, err := runtime.Mark(ctx, managed)
ref := tracing.TraceRef{Kind: "http", Stream: managed.Session().Snapshot().Events.Stream, AfterSeq: before, ToSeq: after}

err = tracing.ReadRecords(ctx, runtime, ref, func(exchange http.Exchange) error { ... })
summary, err := runtime.Summarize(ctx, ref)
err = runtime.Export(ctx, ref, "har", w)
```

- **`Mark`** checkpoints the session, so everything the capture emitted before it is committed, and returns the stream's committed high seq: 0 for a stream that has committed nothing yet.
- **`TraceRef`** names the rows after `AfterSeq` through `ToSeq`; a `ToSeq` of 0 runs to the stream's end. `ref.Params()` are the `traces/<kind>` profile params (`stream`, `afterSeq`, `toSeq`) that read the same window.
- **Reads.** `Runtime.Read` hands each row as stored with its seq; `ReadRecords[R]` decodes them as the kind's `Records` do, refusing an `R` that is not the kind's record type.
- **Refusals.** A ref without a kind or stream, or whose window ends before it starts, is `ErrInvalidRef`; an unregistered kind is `ErrUnknownKind`; a stream that does not exist, or holds another kind's records, is `recordstore.ErrNotFound`; an export format the kind lacks is `ErrUnknownFormat`.
- **Keyed kinds.** A kind whose later rows replace earlier ones by key, such as `process_tree`, moves a replaced row to its replacement's seq, so a window holds the rows that last changed within it.

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
| `WithTruncation(maxBytes)` | Cuts every text or JSON string longer than `maxBytes` and lists the cut paths in a `truncated` column. The record must embed `tracing.Truncation`. |
| `WithJSONProcessor(paths...)` | Parses the text fields at `paths` (dotted JSON names such as `response.content.text`) when they hold a JSON object or array, so they are stored, masked and filtered as structure. Each path must name a string field of the record, or the kind is refused. `Records.Scan` hands that JSON back as text, re-encoded, so key order and spacing can differ from what was emitted. |
| `WithSecretMasking(keep...)` | Masks sensitive keys and HAR name/value pairs, and strips secrets from every other string. Keys named in `keep` are never masked. |

Two more ways a kind can avoid duplicates:
- **Key column.** A kind with a `KeyColumn` stores each key once per stream. The store skips a repeated key, and a page never names one key twice: the session summary's `collapsed` counts the copies a page left out.
- **Retries.** A retried append is idempotent through the probe's cursor, separately from content deduplication.

## Serving kinds

```go
kinds := tracing.NewKinds()
kinds.RegisterKind("heartbeat", tracing.NewHandler(heartbeats{}, tracing.Capabilities{Live: true}))

results, _ := recordresults.Open(recordresults.OpenOptions{
	Prefix: "traces", ConnectionName: "traces", Settings: settings,
	Register: kinds.RegisterResultTypes, // declares every kind's result type
})
runtime := &tracing.Runtime{Kinds: kinds, Results: results, Probes: probe.NewManager(probe.ManagerOptions{LockDir: lockDir})}
runtime.Sessions = query.NewSessionRegistry(query.RegistryOptions{
	BeforeRead: results.Registry.BeforeRead,
	Restarters: map[string]query.RestartFunc{tracing.ProfilePrefix: runtime.Restarter(baseContext)},
})

managed, err := runtime.Start(ctx, tracing.StartRequest{Kind: "heartbeat", Params: json.RawMessage(`{"every": "5s"}`)})
```

- **Errors.** `Start` refuses an unknown kind with `ErrUnknownKind` and params the kind refuses with `ErrInvalidParams`.
- **Profile.** Each session runs as profile `traces/<kind>`, which keys its authorization and its restart.
- **Records.** `SessionStatus.Events` names the stream; read it through the `traces/<kind>` profile with `stream` set to it.

`Kinds.List()` describes each kind for a client: its params schema with defaults, its columns, its capabilities and its `exportFormats`.

## Kinds a host serves over its own transport

Some kinds capture from a system commons-db cannot reach by itself. commons-db owns the capture and the records; the host provides the transport and its own params form. `query serve` does not register these kinds.

| Kind | Records |
| --- | --- |
| `jfr` | A JVM profile from a JFR recording, made by the JDK's Flight Recorder (`jfr` engine, JDK 8u262+) or by async-profiler (`async-profiler` engine). Each record is one stack's samples of an event within one dump: `cpu` and `wall` counted in samples, `alloc` weighed in bytes, `lock` (contended monitor enter) and `park` in nanoseconds blocked. Split by thread when asked. |

`jfr.NewKindFor[P](recorder)` serves the `jfr` kind over a host's params type `P`, whose `JFROptions()` maps the form onto `jfr.Options`:
- **Engine and events.** The `jfr` engine records every event its `Settings` (a predefined configuration such as `profile`) enables. async-profiler profiles one `Event` per session: `cpu`, `wall`, `alloc` or `lock`. `Options.Validate` refuses anything else; a host's `Validate` should call it, so a start is refused before a session exists.
- **Transport.** The host's `jfr.Recorder` starts a recording named after the session. The returned `jfr.Recording` dumps the whole recording so far as an uncompressed `.jfr` and stops it. The session is named after `Recording.Handle()` and described with `Recording.Metadata()`.
- **Dumps.** The kind dumps the recording at each checkpoint, every `Options.Every` when set, and once more as the capture stops. It stores only the events that ended after the previous dump's latest one, so overlapping dumps never store a sample twice.
- **Report.** It reports a `jfr.Report` (dumps, bytes and samples by event, and its watermark). A recording that cannot be stopped is the session's error.

`decode.Decode` (package `tracing/jfr/decode`, which imports nothing else of commons-db) is the same decoder on its own: it reads a `.jfr` of one or more chunks into samples aggregated by event, thread and collapsed stack (`root;…;leaf`, with dotted frames such as `java.lang.Thread.run`). It refuses a malformed recording, or a stack, frame or thread that is missing from its chunk's constant pool, rather than skipping it.

## The query server

`query serve` serves these kinds:

| Kind | Records |
| --- | --- |
| `http` | Outbound HTTP exchanges of chosen features (`prometheus`, `loki`, `http`, ...), one HAR entry each. |
| `sql_statements` | SQL statements commons-db runs on the connections it names, as `sqlstatements.StatementRow`s. `"*"` names every connection and `"self"` the server's own database; `"*"` does not include `"self"`, and naming none is refused. Each row carries its `op` and `tables`, derived from its text by `query/sqltext`, as `xetrace` classifies an event's, and a `repeatKey` that statements differing only by their values share. The session reports a `sqlstatements.Summary`: cost, failures, rows, tables, counts by op, and the statements that ran more than once (an N+1). Its profile filters by op, tables, connection and driver, searches the SQL and error, and serves those repeats as the `traces/sql_statements/repeats` view. A host registers it as `sqlstatements.RowKind`. |
| `sql_xevent` | SQL Server Extended Events, as `sqlserver/xesession` event rows, deadlock reports decoded (`xesession.DecodeEvent`). The session is named after its XE session, reports an `xetrace.Summary` and warns of events it lost. A host with its own params form serves it with `xevent.NewKindFor`, over a params type whose `XEventOptions()` names the connection, the XE session and its drain. |
| `opensearch` | Span documents of an opentelemetry connection's index, over a window and optionally followed. |
| `process_tree` | Each process under a root (`rootPid`, zero for the host itself) as last seen, keyed by pid and start second (`process.Key`) and replaced as it changes: its command, working directory, status, CPU and RSS with their peaks, and when it exited. `key` and `parentKey` form its hierarchy, so a `rootsOnly` read returns the root. At each checkpoint the kind observes the tree and dumps the root's goroutines onto its node (`stack`); a root other than the host records why it cannot in `stackError`. The capture ends by itself once the root exits. |
| `http_proxy` | Every exchange a child process sends through a recording proxy the capture listens on (`127.0.0.1`, an ephemeral port), as `http.Exchange` rows. Its exports point a child at it: `HTTP(S)_PROXY`, `NO_PROXY` (the proxy and `noProxy`) and, with `mode: mitm`, the CA trust variables (`SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, …) for an ephemeral CA written to a file the capture removes. `mode: connect` (the default) records each TLS tunnel's host and byte counts; `mitm` decrypts it, verifying the upstream against the system roots plus `upstreamCA` (a PEM file, read at start); an upstream it does not trust is answered 502 and recorded with its `error`. `hosts` and `paths` globs scope what is recorded, `bodies` caps a stored body (default `256KiB`), and `redact` adds headers to the built-in denylist. It chains to the host's own `HTTPS_PROXY`. |
| `pg_proxy` | Every statement a child process runs through a postgres wire proxy in front of `dsn` (`$VAR` expanded, an unset one failing; else `GAVEL_DB_DSN`, then `DATABASE_URL`), as `sqlstatements.StatementRow`s. Its exports are the rewritten `DATABASE_URL`/`GAVEL_DB_DSN` and `PGHOST`, `PGPORT`, `PGSSLMODE=disable`, `PGDATABASE`, `PGUSER`. Bind values are kept only with `params: true`; TLS to the proxy is refused, and past `maxStatements` (20000) it warns and stops storing. |
| `terminal` | A terminal session its host feeds: the host writes PTY output to `terminal.Writer(stream)` and reports `Resize` and `SetExitCode`, and the capture stores output, resize, settled-screen snapshots and the exit as `terminal.Event` rows. `serve` never feeds one; it serves the kind so a host's store (`--traces-dir`) reads and exports as `cast`. |
| `process_samples` | Every process of the same tree at each tick (`interval`, default `1s`, at least `250ms`): CPU, memory share and RSS, keyed as its `process_tree` node, and followed. |

Its routes:
- `GET /api/v1/traces/kinds` lists the kinds.
- `POST /api/v1/traces/{kind}/sessions` starts a session, with body `{"params": {...}, "duration": "5m"}`.
- `GET /api/v1/traces/{kind}/export?stream=&afterSeq=&toSeq=&format=` downloads a window in one of the kind's `exportFormats`, as an attachment named `<kind>-<stream>.<ext>`. It is authorized as reading the `traces/<kind>` profile. It answers 404 for an unknown kind or stream, and 400 for no stream, no format, a format the kind lacks or a window that is not one. A failure once the download has begun aborts the response, so a client never keeps a file cut short.
- `/api/v1/sessions/{id}/...` stops, extends, restarts and streams it.

`/api/openapi.json` describes one start operation per kind, whose request body is that kind's params form, and one export operation per kind.

`query serve` opens its trace store at `<config-dir>/traces`. `--traces-dir <dir>` opens another one instead, such as a gavel run's `.gavel/traces/<run>`, so its captures can be browsed in the query SPA.

The routes authorize nothing themselves. A host that serves them sets `sessions.Options.Authorize`, which is asked for every start, read and control of a session under its `traces/<kind>` profile; without it, as in `query serve`, anyone who reaches the server can start a capture. A host such as oipa authorizes its users in front of these routes.

A refused start is answered with its error as text: 404 for an unknown kind, 409 at the session limit, 400 otherwise. A host that can say more about a refusal sets `sessions.Options.TraceStartError`. It is given the error first, and when it returns true its own response stands. oipa uses it to answer a missing SQL Server grant with a 403 that carries the grant statements.

The `http` and `sql_statements` kinds observe commons-db through taps in the `connection` package:
- `ObserveHTTP` hands a HAR collector every exchange of a feature, whatever its HAR level.
- `ObserveSQL` hands an observer every statement published for a connection, for every connection (`EveryConnection`), or for the server's own database (`OwnDatabase`).

Both are process-wide: a session sees every caller's traffic for what it observes.

The process kinds share a `process.Sampler`, so the tree and samples captures of one root list the host's processes once a tick (`ps` on unix). A host registers both over one sampler: `process.NewTreeKind(sampler, process.TreeOptions{})` and `process.NewSamplesKind(sampler)`. `TreeOptions.Stack` replaces how the root's stacks are dumped.
