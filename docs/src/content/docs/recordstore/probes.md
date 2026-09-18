---
title: "Probes: cursor sources"
description: Drain a cursor-based external source into a record stream with one exclusive writer, ordered appends and a sealed finish.
---

`github.com/flanksource/commons-db/recordstore/probe` connects **cursor-based external sources** to record streams. Examples are an agent's ring buffer, a database event session, or anything else you page by position. The source adapter owns transport and decoding. The probe package owns exclusive writers, cursor fences, append ordering and finalization.

## The Source interface

```go
type Source interface {
	Read(ctx context.Context, cursor Cursor) (Batch, error)
	Freeze(ctx context.Context) error          // stop new source writes
	Finalize(ctx context.Context) (Final, error) // rows materialized only after freezing
	Release(ctx context.Context) error         // give up the remote ownership claim
}

type Cursor struct {
	Generation string `json:"generation,omitempty"`
	Next       int64  `json:"next"`
}

type Batch struct {
	Generation string            // the source's incarnation; must not change mid-run
	Next       int64             // the cursor after this page
	Write      int64             // the source's current write cursor
	More       bool              // request another page in this sample
	Active     bool              // the source is still producing
	Rows       []recordstore.Row
	Summary    any
}
```

Every batch is checked before its rows are appended. The run fails if:

- the generation is empty, or differs from the cursor's
- `Next` moves backwards
- `Write` is behind `Next`
- `More` is set but the cursor did not advance

Optional interfaces extend a source:

| Interface | Called |
| --- | --- |
| `Committer.Commit(ctx, Batch)` | after a batch is durable, to advance source-local decoding state. A retry must be safe, because a process can fail between the append and the commit. |
| `FinalCommitter.CommitFinal(ctx, Final)` | after the final rows are durable and before the stream is sealed |
| `Renewer.Renew(ctx)` | every `Options.RenewEvery`, to extend an ownership lease. Required when `RenewEvery > 0`. |
| `Detacher.Detach(ctx)` | on `Run.Detach`, to release local resources without touching the source |

## Arming a probe

A `Manager` admits **at most one local writer per source identity**:

```go
manager := probe.NewManager()

run, err := manager.Arm(ctx, probe.Options{
	Identity: "jvm:" + host + ":" + probeID, // one writer per identity
	Stream:   "probe-" + probeID,
	Kind:     "jvm_trace",
	Backend:  results.Backend,                  // anything with Append and Seal
	Describe: func(ctx context.Context, stream string) (*query.EventsRef, error) {
		ref, err := results.Ref(ctx, stream, 0, 0)
		if err != nil {
			return nil, err
		}
		return ref.EventsRef(), nil
	},
	Cursor:     previous.Cursor, // resume a predecessor's checkpoint, or zero
	MaxPages:   64,              // 0 → DefaultMaxPages
	RenewEvery: 30 * time.Second,
}, func(ctx context.Context) (probe.Source, error) {
	return openAgentSource(ctx, host, probeID)
})
```

`Arm` does the following, in order:

1. reserves the identity. A second `Arm` for the same identity fails with `ErrAlreadyManaged`.
2. opens the source
3. creates the empty stream
4. describes it

`Arm` returns only once the stream's first durable reference exists. If any step fails, the source is released and the reservation dropped.

## Running

`*probe.Run` implements `query.ManagedRun`, so a managed session can drive it:

- **`Sample(ctx)`** reads pages until the source's cursor catches its write cursor, appending each page's rows and committing its cursor. It fails if the source is still behind after `MaxPages` pages.
- **`Stop(ctx)`** freezes the source, drains it, appends the `Final` rows, commits them, **seals the stream**, and only then releases the source. Followers of the stream finish once they've read through it.
- **`Detach(ctx)`** ends local ownership without freezing, finalizing, sealing or releasing the source, so a successor can take over.
- **`ProbeStatus()`** returns the restart checkpoint (`Cursor`, `Appended`, `Skipped`, `SourceActive`) that a successor passes back through `Options.Cursor`.
- **`Finished()`** is closed once the source reports it's no longer active, or the run ends.

A kind with a `KeyColumn` makes a successor safe to restart from an older cursor. For example, a restarted follower that replays an agent ring it already stored skips the rows whose keys the stream holds. See the [JVM probe example](../examples/#jvm-probe-watches-and-traces).
