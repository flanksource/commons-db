---
title: Concepts
description: Streams, kinds, seqs, windows, generations, keys, retention, trimming, expiry and sealing.
---

`github.com/flanksource/commons-db/recordstore` keeps **append-only record streams**: the rows a long capture produces, written as they arrive and read back by position. A report doesn't carry those rows inline. It points at them by stream and seq window.

## Rows

A row is a `recordstore.Row`, an alias of `query.Row`: a JSON-shaped `map[string]any` keyed by column name. The easiest way to append typed values is `AppendTyped`, which round-trips each item through `encoding/json`:

```go
result, err := recordstore.AppendTyped(ctx, backend, "run-1", "sql_event", events)
```

`EncodeRow` and `DecodeRow` keep numbers as `json.Number`, so an `int64` too large for a `float64` survives the trip.

## Streams and kinds

A **stream** is named by a stream id and holds rows of **one kind**.

| | Rule | Why |
| --- | --- | --- |
| stream id | 1–200 characters of `A-Z a-z 0-9 . _ : -`, and not `.` or `..` | An id becomes a Redis key segment, a file name and a query parameter. The alphabet leaves nothing any of them has to escape. |
| kind | starts with a letter, then letters, digits and `_`, up to 63 characters | A kind names a SQLite table (`records_<kind>`), a directory and a profile. |

The first append to a stream creates it under the append's kind. Appending to an existing stream under a different kind is an error.

## Seqs and windows

Every row gets a per-stream **seq**, contiguous from 1. The seq is the only position a reader resumes from. Timestamps and keys never are, because neither orders nor identifies a row's position reliably.

A `Window` is an inclusive seq range `{From, To}`. An empty window has `From == To+1`, so an append of no rows reports the seq the next row will take.

```go
type AppendResult struct {
	Window  Window // the seqs this append's rows were numbered into
	Skipped int64  // rows skipped because their key was already stored
}
```

## Stream metadata

`Backend.Meta(ctx, stream)` returns a `Meta`:

| Field | Meaning |
| --- | --- |
| `Stream`, `Kind` | the stream id and its kind |
| `Generation` | a UUID for this incarnation of the stream id |
| `Total`, `LowSeq`, `HighSeq` | rows held, and the seqs of the first and last. An empty stream has `LowSeq == HighSeq+1`. |
| `UpdatedAt` | when it was last written |
| `ExpiresAt` | when the stream is removed, or `nil` when it's kept until something expires it |
| `Capped` | an append was refused for capacity: complete up to `HighSeq`, missing that append |
| `Sealed` | the writer declared it complete |

All three counters are reported because an index mirroring a stream can lag behind its source.

### Generations

When a stream expires and its id is reused, the new stream starts again at seq 1 under a **new generation**. A reader holding seqs from the old generation must not read them against the new one. `Notifier.Wait`, `Tail` and the `Indexer` compare generations and report a recreated stream as `ErrNotFound`.

## Keys: idempotent re-ingest

A kind may declare a **key**, a string column that identifies a row within a stream. A keyed stream holds each key once. An append skips every row whose key the stream already holds, atomically with the append, and numbers only the rows it keeps. That's what makes re-reading an overlapping source window safe.

- A row whose key is missing, empty or not a string refuses the whole append.
- One batch naming the same key twice is refused whole.
- Once a row is trimmed away, its key can be appended again.

## Retention

A kind picks one of two retention policies:

| `Retention` | Behaviour | Use it for |
| --- | --- | --- |
| `RetainStream` (default) | The whole stream lives until it expires: the backend TTL after its first append, unless `Expire` moves it. | A bounded capture: one trace, one run |
| `RetainRows` | Each row lives the backend TTL after its own append. Every append slides the stream's expiry to TTL-from-now and trims rows appended longer ago than the TTL, in the same write. | A stream that accumulates for as long as something writes to it |

`RetainRows` requires a backend with a TTL. Without one, nothing would ever leave the stream, so the append is refused.

## Leaving a stream

Rows only leave a stream **from its low end**:

- `Trim(ctx, stream, before)` removes every append made before `before`. The rows kept keep their seqs, and `LowSeq` moves to the first of them.
- `Expire(ctx, stream, ttl)` removes the whole stream `ttl` from now. The TTL must be positive.
- `Delete(ctx, stream)` removes the stream and its rows immediately.

A `Scan` from below the low seq starts at the low seq.

## Sealing

`Seal(ctx, stream)` marks a stream complete. After that:

- every `Append` fails with `ErrSealed`
- `Scan`, `Trim` and `Expire` still apply
- a follower that has read through `HighSeq` returns instead of waiting for more

Sealing a sealed stream does nothing. The seal ends with the stream, so an id reused after expiry starts unsealed.

## Errors

| Error | Returned when |
| --- | --- |
| `ErrNotFound` | the stream doesn't exist, no longer does, or was recreated under another generation |
| `ErrCapacity` | the stream, or one row of it, would exceed what the backend was configured to hold. **None** of the refused append's rows are written. |
| `ErrSealed` | the stream was sealed |

Match them with `errors.Is`. Backends wrap them with the stream id and context.

## The single-writer contract

**A stream has one writer at a time.** Backends serialize appends made within one process with `StreamLocks`. Two processes appending to the same stream is outside the contract. Route each stream to exactly one writer. The [probe manager](../probes/) enforces this for cursor-based sources.

## The Backend interface

```go
type Backend interface {
	Append(ctx context.Context, stream, kind string, rows []Row) (AppendResult, error)
	Meta(ctx context.Context, stream string) (Meta, error)
	Scan(ctx context.Context, stream string, afterSeq int64, fn func(seq int64, row Row) error) error
	Trim(ctx context.Context, stream string, before time.Time) (Meta, error)
	Expire(ctx context.Context, stream string, ttl time.Duration) error
	Seal(ctx context.Context, stream string) error
	Delete(ctx context.Context, stream string) error
	Close() error
}
```

`Scan` calls `fn` with every row after `afterSeq` in seq order, and stops at the first error `fn` returns. To resume a reader, pass the last seq it processed.
