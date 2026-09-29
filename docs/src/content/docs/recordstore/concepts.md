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

Every row gets a per-stream **seq**, increasing from 1. The seq is the only position a reader resumes from. Timestamps and keys never are, because neither orders nor identifies a row's position reliably. Seqs are contiguous unless the kind replaces stored rows: a replaced row's seq is left behind (see [Replacing stored rows](#replacing-stored-rows)).

A `Window` is an inclusive seq range `{From, To}`. An empty window has `From == To+1`, so an append of no rows reports the seq the next row will take.

```go
type AppendResult struct {
	Window   Window // the seqs this append's rows were numbered into
	Skipped  int64  // rows skipped because their key was already stored
	Replaced int64  // stored rows the appended rows replaced
}
```

## Stream metadata

`Backend.Meta(ctx, stream)` returns a `Meta`:

| Field | Meaning |
| --- | --- |
| `Stream`, `Kind` | the stream id and its kind |
| `Generation` | a UUID for this incarnation of the stream id |
| `Total`, `LowSeq`, `HighSeq` | rows held, a lower bound of the first row's seq, and the last row's seq. `Total` is `HighSeq-LowSeq+1` unless the kind replaces stored rows. An empty stream has `LowSeq == HighSeq+1`. |
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

### Replacing stored rows

With `KindOptions.OnConflict: recordstore.OnConflictReplace`, an append of a stored key **replaces** the stored row instead of skipping the new one. The stored row is removed and the appended row is numbered after the high seq in the same write, so a reader following the stream sees the new row. `AppendResult.Replaced` counts the rows removed.

The replaced row's seq is left behind, so the stream's seqs skip it. `LowSeq` stays a lower bound of the first row held, and `Total` counts the rows actually held.

Replacing needs a key, and only the sqlite backend stores such kinds. kv and ndjson address rows by contiguous seqs, so they refuse a replacing kind with `ErrUnsupported`.

## Retention

A kind picks one of two retention policies:

| `Retention` | Behaviour | Use it for |
| --- | --- | --- |
| `RetainStream` (default) | The whole stream lives until it expires: the backend TTL after its first append, unless `Expire` moves it. | A bounded capture: one trace, one run |
| `RetainRows` | Each row lives the backend TTL after its own append. Every append slides the stream's expiry to TTL-from-now and trims rows appended longer ago than the TTL, in the same write. | A stream that accumulates for as long as something writes to it |

`RetainRows` requires a backend with a TTL. Without one, nothing would ever leave the stream, so the append is refused.

## Leaving a stream

Rows leave a stream **from its low end**, or when a replacing kind stores a new row under their key:

- `Trim(ctx, stream, before)` removes every append made before `before`. The rows kept keep their seqs, and `LowSeq` moves to the first of them.
- `Expire(ctx, stream, ttl)` removes the whole stream `ttl` from now. The TTL must be positive.
- `Delete(ctx, stream)` removes the stream and its rows immediately.

A `Scan` from below the low seq starts at the low seq.

## Compaction

A kind's `Compact` rules let the sqlite backend drop rows from the **middle** of its streams, not only from the low end:

```go
recordstore.KindOptions{TimeColumn: "at", Compact: []recordstore.CompactRule{
	{Where: `row.level == "debug"`, OlderThan: time.Hour},
}}
```

A row is dropped when any rule selects it:
- **`Where`** is a CEL expression over the row, bound as `row`.
- **`OlderThan`** selects rows whose time column (which it requires) is older than that.
- A rule that sets both selects the rows matching both.

The backend compacts on every sweep, reading at most 10,000 rows per sweep. `Compact(ctx)` runs one pass now.

- **The last row always stays.** It keeps the high seq, so no seq is ever reused.
- **Seqs skip.** `Total` counts the rows held and `LowSeq` moves to the first one left. A dropped row's key may be appended again.
- **The index follows.** Each compaction that drops rows bumps `Meta.Compactions`. An Indexer seeing the count change drops the same rows from its index (`Index.DeleteSeqs`).
- **kv and ndjson refuse the kind.** They number rows contiguously, so a compacting kind gets `ErrUnsupported`, as a replacing one does.

`Merge(ctx, target, sources, MergeOptions{Where, DeleteSources})` (the `recordstore.Merger` interface, implemented by sqlite) copies several streams of one kind into a new stream:
- **Order.** Rows are ordered by the kind's time column, then by source order, then by seq.
- **Shared keys.** A key several sources hold keeps its first row, or, for a replacing kind, its last.
- **Filtering.** `Where` keeps only the rows it selects.
- **Sources.** `DeleteSources` removes the sources in the same transaction.
- **Retention restarts.** The merged rows are appended at merge time, so a kind that retains rows keeps them from the merge, not from their first append.

## Sealing

`Seal(ctx, stream)` marks a stream complete. After that:

- every `Append` fails with `ErrSealed`
- `Scan`, `Trim` and `Expire` still apply
- a follower that has read through `HighSeq` returns instead of waiting for more

Sealing a sealed stream does nothing. The seal ends with the stream, so an id reused after expiry starts unsealed.

Every backend also implements `recordstore.Reopener`. `Reopen(ctx, stream, generation)` resumes a sealed stream, but only while it still holds the generation the caller recorded. The next append continues after `HighSeq`, and the rows already stored don't change. That holds for a kind that replaces rows too: the seqs they left behind stay skipped. A reader that saw the seal must fetch `Meta` again before it waits for more, and a derived index reopens when its source does.

## Errors

| Error | Returned when |
| --- | --- |
| `ErrNotFound` | the stream doesn't exist, no longer does, or was recreated under another generation |
| `ErrCapacity` | the stream, or one row of it, would exceed what the backend was configured to hold. **None** of the refused append's rows are written. |
| `ErrSealed` | the stream was sealed |
| `ErrUnsupported` | the backend can't store the kind as declared, such as a replacing kind in kv or ndjson |
| `ErrSchemaConflict` | a durable sqlite file stores the kind with another key, conflict policy or column storage than the rows' schema declares |

Match them with `errors.Is`. Backends wrap them with the stream id and context.

## Batches

A backend that implements `recordstore.BatchAppender` (sqlite does) applies several writes at once. A `Batch` has an `ID`, the `Producer` that made it (a per-process `Instance` and a `Seq` counting its batches), optional `Schemas`, and `Entries`. Each entry is one operation on one stream: `append` (optionally sealing after), `seal`, `expire`, `trim`, `delete` or `reopen`. An entry that names a `Generation` applies only to that incarnation of its stream; any other fails it with `ErrNotFound`.

`AppendBatch` applies a batch in one transaction:

- Entries run in order, each under its own savepoint. An entry the store refuses (sealed, not found, a schema conflict, an invalid request) rolls back alone. Its `EntryResult.Error` is a `*BatchError` whose `Code` says why, and it unwraps to the matching sentinel for `errors.Is`. The other entries still commit.
- The outcome is recorded under the batch id in the same transaction. Applying the same id again returns the recorded outcome and writes nothing. `BatchOutcome(ctx, id)` reads it back.
- A kind the batch declares in `Schemas` is reconciled additively with the kind the file stores: added columns are added. The backend's own schema resolver is never changed.
- Rows count as appended when the batch is applied, so a kind that retains rows keeps them from that moment.

An error returned by `AppendBatch` itself means nothing was applied. A `Notifier` wrapping a batch appender wakes the followers of every stream the batch names.

## One writer per store

**A store has one writer process at a time.** Backends serialize the writes made within one process with `StreamLocks`. Several processes can share a store only by opening it through [`recordstore/owner`](../multi-process/), which elects one owner to write it. The other processes read it and hand their writes to the owner over the spool. Two processes writing one store any other way is outside the contract. The [probe manager](../probes/) keeps each cursor-based source to one writer.

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
