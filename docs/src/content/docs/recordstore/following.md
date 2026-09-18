---
title: Following and indexing
description: Wake readers as appends commit with the Notifier, tail a stream, and mirror any backend into a sqlite index with the Indexer.
---

## Notifier

`recordstore.Notifier` wraps a backend and wakes the readers waiting on a stream **as soon as an append commits**, so a reader can follow a stream instead of polling it. It delegates every call to the backend it wraps.

```go
notifier, err := recordstore.NewNotifier(backend, recordstore.NotifierOptions{
	RecheckInterval: 5 * time.Second,
})
```

Only appends made **through the Notifier** wake a waiter at once. That covers the contract, because a stream has one writer process. `RecheckInterval` (required) is how often a waiter re-reads the metadata while nothing wakes it. The recheck is what notices events no append announces: a stream expired, trimmed away, or removed by the backend itself.

`Trim`, `Expire`, `Seal` and `Delete` also wake the stream's waiters, which then re-read what's left.

Wrap a backend once. `NewNotifier` refuses a backend that is already a Notifier. `Unwrap()` returns the wrapped backend.

### Wait

```go
meta, err := notifier.Wait(ctx, stream, afterSeq, generation)
```

`Wait` blocks until the stream holds a row after `afterSeq` or is sealed, and returns its metadata. A sealed stream whose `HighSeq <= afterSeq` has nothing more to wait for.

`generation` is the incarnation the caller has read, and it's required. A stream that is gone, or that was recreated under another generation, returns `ErrNotFound`, because the caller's seqs no longer name its rows. A cancelled context returns `ctx.Err()`.

`Wait` subscribes before reading the metadata, so an append that commits between the read and the wait still wakes it.

### Tail

```go
err := notifier.Tail(ctx, stream, afterSeq, func(seq int64, row recordstore.Row) error {
	return emit(seq, row)
})
```

`Tail` calls `fn` with every row after `afterSeq`, then with each row appended after that as it arrives. It returns:

- `nil` once `ctx` ends, which is how a tail is stopped, or once it has read through a sealed stream's `HighSeq`
- `fn`'s first error
- an `ErrNotFound` error when the stream doesn't exist or stops existing: expired, removed, or recreated as a new generation

`recordresults.Results.Backend` is a Notifier, and it satisfies `sessions.EventRecords` (`Meta`, `Scan`, `Tail`), which is what the sessions service replays and follows a session's events through.

## Indexer

An `Indexer` keeps a **sqlite index** caught up with a source backend, one stream at a time and incrementally by seq. Each `Ensure` copies only what the source gained since the last one. That's what makes a stream written to kv or ndjson by one process pageable through another process's query engine.

```go
indexer, err := recordstore.NewIndexer(source, index)
if err := indexer.Ensure(ctx, stream); err != nil { … }
```

`index` is a `recordstore.Index`: a backend that takes rows under the seqs another backend gave them. `*sqlite.Backend` is the one implementation. The pairing is checked at construction:

| source vs index | index `Derived` | allowed? |
| --- | --- | --- |
| the same backend | `false` | yes, and `Ensure` only confirms the stream exists |
| the same backend | `true` | no: rebuilding it would discard the authoritative rows |
| separate | `true` | yes |
| separate | `false` | no: a separate index must be derived so it can be rebuilt |

### What Ensure does

1. **Prepare**: reconcile the kind's table and read the indexed incarnation. A derived index drops an older generation stored under the same stream id.
2. **Catch up**: mirror the source's trim (`TrimBelow`), then import every row after the indexed high seq in batches of 500 (`Import`). Seqs must arrive without a gap and must reach the source's high seq. A scan that ends early is refused, never taken as the whole stream, because an index that stopped short would page the stream as complete.
3. **Re-check the generation**: if the source was recreated while it was being indexed, fail.
4. **Mirror trim, expiry and seal**: the index takes the source's exact `ExpiresAt` (`SetExpiry`), so an index entry never outlives its stream. It seals once it holds every row of a sealed source.

`Ensure` holds a per-stream lock, so concurrent reads of the same stream catch up once.

You rarely call the Indexer yourself. `recordresults.Registry.BeforeExecute` calls `Ensure` for every stream a read names, then holds an index lease across the read. See [Serving over HTTP](../../recordresults/serving/).
