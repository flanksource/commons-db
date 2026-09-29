---
title: Multiple processes
description: One elected owner writes a store; every other process reads it and hands its writes over the spool.
---

More than one process can open the same record store: a `query serve`, a probe host, a CLI writing one capture and exiting. SQLite allows one writer at a time, and two processes numbering seqs in one stream would collide. So `recordstore/owner` **elects one process, the owner**, to write each store. Every other process reads the store and hands its writes to the owner. When the owner exits, the first process to take its lock takes over.

## Files beside a store

Everything is derived from the path the store is **configured** at, `P`, not from the versioned file (`<dir>/v6/records.sqlite`). So there is one owner across builds, and the owner tells the others which file is live.

| Path | Role |
| --- | --- |
| `P.lock` | The flock that elects the owner. It is the **only** truth about who owns the store. The kernel releases it when the owner dies, however it dies. |
| `P.owner.json` | The owner's advisory state: instance, pid, host, build, phase (`starting`, `ready`, `draining`), heartbeat, the live file and its catalog version, the spool and its formats, the socket, backlog and failure counts. It is written through a temp file and renamed into place. |
| `P.sock` | The owner's control socket. When `P.sock` would exceed a socket address's length limit, it lives in the temp dir as `recordstore-<hash>.sock`. The state names it either way. |
| `P.spool/` | The spool: `tmp/`, `incoming/`, `failed/`, `trash/`, private to the user. |

`Open` refuses a store on a network filesystem (NFS, SMB/CIFS, 9p, FUSE, virtiofs, Ceph; on macOS smbfs, afpfs, webdav and FUSE). File locks and SQLite's WAL are unreliable there, so two processes could both believe they own the store.

## Opening a store

```go
store, err := owner.Open(ctx, owner.Options[*sqlite.Backend]{
	Path:           path,
	Store:          sqlite.VersionedPath(path),
	CatalogVersion: sqlite.CatalogVersion,
	Build:          version,
	Open: func(ctx context.Context, readOnly bool, submit recordstore.Submitter) (*sqlite.Backend, error) {
		return sqlite.Open(sqlite.Options{Path: path, Schema: schemas.Kind, TTL: ttl,
			SweepInterval: 10 * time.Minute, ReadOnly: readOnly, Submit: submit})
	},
})
backend, err := store.Backend()
```

`recordresults.OpenSharedSQLite(ctx, sqlite.Options{...}, build)` does exactly this and returns the backend, whose `Close` releases the hold. Use it wherever a store is kept in a local sqlite file, including a `Router`'s `Open` function for its routes. `recordresults.Open` uses it for its own sqlite store.

- **The process that takes the lock owns the store.** It publishes its state as `starting`, opens the backend writable, serves the control socket, publishes `ready`, and ingests the spool.
- **Every other process is a reader.** It waits for the owner to be `ready` (up to `StartWait`), opens the same file **read-only**, and waits for the lock in the background. Reads (`Meta`, `Scan`, profile queries) go straight to the file; WAL makes that safe while the owner writes.
- **When the lock comes free, a reader takes over.** It promotes its backend in place (the same `*sqlite.Backend` keeps working), publishes its state and starts ingesting. `OnRole` tells it.
- **Within one process, every `Open` of a path shares one `Store`.** The last `Close` releases it. A process never waits on its own lock or spools to itself.

A reader whose build reads another catalog version than the owner's can't read the file. `Backend()` returns `owner.ErrCatalogVersion`, but its writes still reach the owner through a `Writer`.

## Writing as a reader

A reader's backend doesn't write the file. Each mutation (`Append`, `Seal`, `Expire`, `Trim`, `Delete`, `Reopen`, `AppendBatch`) is:

1. **checked locally** where it can be: the stream's kind and seal, the kind's columns and keys;
2. **published** to the spool as a one-entry batch (see below);
3. **nudged** to the owner over its socket, which only saves the owner's poll;
4. **awaited** in the owner's batch ledger, and returned exactly as the owner applied it: the same `AppendResult`, or the same sentinel (`ErrSealed`, `ErrNotFound`, `ErrSchemaConflict`, ...).

If the caller's context ends first, the write returns a `*owner.PendingError` (`errors.Is(err, owner.ErrPending)`). The batch is durable and will be applied; `store.Await(ctx, err.BatchID)` reports how. **Don't blindly retry an unkeyed append**: it would be stored twice.

A kind the reader declares but the file lacks is declared through the owner too. A reader's `Table(kind)` submits a batch of no entries carrying the schema, and adopts the table once the owner has created it.

A spooled write costs tens of milliseconds. A process writing a lot should use the bulk writer:

```go
writer, err := store.Writer(schemas.Kind, spool.WriterOptions{})
writer.Append(ctx, stream, kind, rows)
writer.Seal(ctx, stream)
err = writer.Flush(ctx, owner.FlushOptions{Wait: true})
```

The owner applies a `Writer`'s batches directly. A reader publishes them and, with `Wait`, waits for them and returns the entries the owner refused. Batches are cut at 50,000 rows or 64MiB, so a bulk write spanning batches isn't atomic. Without `Wait`, a CLI can exit as soon as `Flush` returns: its batches are durable in the spool, and the owner, or the next one, ingests them.

## The spool

`recordstore/spool` is the only way writes cross processes. A batch is published by writing it under `tmp/`, syncing it, and **renaming** it into `incoming/`. That rename is its only commit point, so the owner never sees half a batch.

- **Layout:** `incoming/<created>-<instance>-<seq>-<id>/`, holding a `manifest.json` (format 1) and one data file per append.
- **Formats:** `ndjson` and `ndjson.gz` are built in. Importing `recordstore/spool/parquet` registers `parquet`, where each column is a nullable string, boolean, int64 or double. Other codecs register with `spool.Register`. The owner reads only the formats its own build registered, and its state lists them.
- **Manifest:** carries the schema of every kind the batch appends to, so the owner can ingest from a build whose kinds differ. Added columns are added to the file. A conflicting key, conflict policy or column storage fails the entry with `ErrSchemaConflict`.
- **Values:** rows are normalized to what the sqlite table stores (`spool.Normalize`) before they are written. A spooled row and the same row appended directly store identical values.

The owner **ingests** the spool oldest first:

- **Exactly once.** Each batch is applied in one transaction, a savepoint per entry, and recorded by id in the ledger (`record_spool_batches`) in that same transaction. It is then moved to `trash/`. If the owner dies between the commit and the move, the next owner sees the batch again and answers it from the ledger.
- **In each producer's order.** Every process numbers its batches. A batch whose predecessor from the same producer hasn't been applied waits for it for up to a minute, then is applied anyway, and the gap is logged.
- **Failures.** A batch the owner can't read at all (an unknown manifest format, a corrupt file, more than a million rows) goes to `failed/` with an `error.json`. The producer awaiting it fails at once. A batch the store fails to apply is retried with backoff from 100ms to 30s, and moved to `failed/` after 20 attempts or 10 minutes.
- **Housekeeping.** Every minute the owner empties `trash/`. It removes `failed/` entries after 30 days and abandoned `tmp/` entries after 24 hours. Ledger rows older than 7 days are swept, unless their batch is still in `incoming/`.

Retention starts when a batch is **ingested**. A kind retaining rows keeps a spooled row for its ttl from then, not from when the producer made it.

## Closing

- **The owner** stops serving its socket, drains the spool (up to `DrainOnClose`), withdraws its state and unlocks.
- **A reader** stops waiting for the lock.
- **Either one then checks the spool again.** If batches are left and nobody else holds the lock, it takes the lock, ingests them and releases it. So a producer that published while the owner was letting go isn't left waiting for the next owner.

## ndjson and derived indexes

These have no read-only mode, so they are held **exclusively**.

- **ndjson files.** `owner.Exclusive(<dir>/ndjson, build)` holds them. `recordresults.Open` does this for its ndjson store. A second process gets a `*owner.LockedError` (`errors.Is(err, owner.ErrLocked)`) naming the holder's pid, host and build.
- **A derived index.** The index `recordresults.Open` mirrors a `Source` into is held the same way. A process that finds `index.sqlite` held indexes the source into a private `<dir>/private/<instance>/index.sqlite` instead, and reports `Role()` as a reader. Private indexes whose holder has gone are removed the next time one is opened.

## What the control socket is for

The control socket speeds things up and reports status; nothing depends on it for correctness. It takes one JSON request per connection:
- `status` returns the owner's state.
- `ingest` with batch ids nudges ingestion; with `wait`, it answers once those batches are applied.

If the socket is gone or unreachable, producers still get their batches applied at the owner's next poll (every 50–500ms).
