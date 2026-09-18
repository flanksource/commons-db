---
title: Stream refs
description: Report where a capture's rows are instead of carrying them — a StreamRef names a stream, a seq window and the store holding it.
---

A capture that runs for minutes can produce hundreds of thousands of rows. Its report, whether a session status, a step result or an API response, shouldn't carry them. It should **point at them**. A `StreamRef` names the stream, the generation the seqs belong to, a seq window, and the store that holds the rows. A reader can replay them after the capturing process is gone.

```go
ref, err := results.Ref(ctx, "job-42", 0, 0) // 0 = the stream's low / high seq
```

```json
{
  "stream": "job-42",
  "kind": "query_event",
  "generation": "3f1c…",
  "low": 1, "high": 1840,
  "from": 1, "to": 1840,
  "total": 1840,
  "expiresAt": "2026-09-25T10:00:00Z",
  "store": { "backend": "sqlite", "host": "build-7", "file": "/var/lib/acme/traces/v4/records.sqlite" }
}
```

| Field | Meaning |
| --- | --- |
| `stream`, `kind` | what to read, and through which profile (`<prefix>/<kind>`) |
| `generation` | the incarnation the seqs belong to. A reader that finds another generation must treat the ref as expired. |
| `low`, `high` | the committed seq bounds when the ref was taken |
| `from`, `to` | the inclusive window the ref names: a step's checkpoint, or `low..high` |
| `total`, `expiresAt` | as `recordstore.Meta` reported them |
| `store` | where the rows are. `backend` is always set. `host` and `file` are set for local sqlite and ndjson files. A kv stream has neither, and is read back through the store the environment names. |

## Windows are strict

`Ref(ctx, stream, from, to)` refuses a window it can't honour, and never clamps one:

- a negative seq
- `from > to+1`
- `from` below the low seq: those rows are trimmed
- `to` past the high seq

An empty window (`from == to+1`) is allowed, which is what an empty stream's window is. A ref promises its rows exist.

To name one step of a capture, record the `AppendResult.Window` each step's append returned and take a ref over that window:

```go
appended, err := recordstore.AppendTyped(ctx, results.Backend, stream, kind, stepRows)
stepRef, err := results.Ref(ctx, stream, appended.Window.From, appended.Window.To)
```

## Reading a ref back

Read it through the type's profile with the ref's window:

```text
GET /api/v1/profile/traces%2Fquery_event?stream=job-42&afterSeq=<from-1>&toSeq=<to>
```

Or scan it directly from a backend that holds the stream:

```go
err := results.Backend.Scan(ctx, ref.Stream, ref.From-1, func(seq int64, row recordstore.Row) error {
	if seq > ref.To {
		return errStop
	}
	return handle(row)
})
```

## Session events

`ref.EventsRef()` converts the ref to a `*query.EventsRef`, the form a session status records as `status.events`. Its JSON is identical, field for field. The sessions service uses it with `sessions.Options.Records` (for example, `results.Backend`) to replay a session's events after its owner process has gone, and to follow them while this process still writes them. The [probe manager](../../recordstore/probes/) calls your `Describe` function to produce exactly this ref.
