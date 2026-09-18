---
title: The sqlite record file
description: How recordstore/sqlite lays out records.sqlite and index.sqlite — catalog tables, versioned paths, durable vs derived files, sweeping and leases.
---

`recordstore/sqlite.Backend` stores record streams in a SQLite file that is **also the query index** a `sql` profile reads. Used as the durable backend (`records.sqlite`), it's its own index. Behind kv or ndjson, it's a **derived** index (`index.sqlite`) that an `Indexer` keeps caught up.

```go
backend, err := sqlite.Open(sqlite.Options{
	Path:          filepath.Join(dir, "records.sqlite"),
	Schema:        schemas.Kind,
	TTL:           30 * 24 * time.Hour, // 0 keeps streams until expired; refuses RetainRows kinds
	Derived:       false,
	SweepInterval: 10 * time.Minute,
	Now:           nil,                 // clock; nil is time.Now
})
```

`Path` must be a file, not `:memory:`, because a profile reads it through a connection of its own.

## Tables

| Table | Holds |
| --- | --- |
| `record_store_format` | one row: the catalog version (currently **4**) |
| `record_streams` | one row per stream: `stream_id`, `generation`, `kind`, `total`, `low_seq`, `high_seq`, `updated_at`, `expires_at`, `capped`, `sealed`. It's indexed on `expires_at` for the sweeper. |
| `record_kinds` | one row per kind: its table name and a JSON catalog of each column's declared name, physical name, stored type and declared type, plus the key |
| `record_appends` | when each append stored its rows, keyed by the seq of its last row. `Trim` finds the rows appended before an instant through it. |
| `records_<kind>` | the rows: `stream_id`, `seq`, then the kind's columns, with primary key `(stream_id, seq)` |

Column names in `records_<kind>` are derived safe names (see [sqlitetable](../sqlitetable/#physical-names)). Read a kind's table through `backend.Table(kind)`, whose `Select()` aliases them back, or through the result profile.

An append runs in **one write transaction**:

1. apply `RetainRows` trimming
2. skip rows whose key is already stored
3. insert the rest numbered after `high_seq`
4. update `record_streams` and `record_appends`

## Durable vs derived

`Derived` decides what happens when the file disagrees with the schema this build declares:

| Situation | Durable (`records.sqlite`) | Derived (`index.sqlite`) |
| --- | --- | --- |
| a kind's columns, types or key changed since its table was created | **error**: those rows exist nowhere else | the kind's table is dropped with every stream it held, and the Indexer refills it |
| the file's catalog version is unsupported, or the catalog is incomplete | **error**: remove the file and rebuild it | every table is dropped and the catalog recreated |
| a stream's generation differs from the source's | **error** | the old generation is removed, and the import starts at seq 1 |

A label or filter change is always allowed. Only changes that would make stored rows mean something else are refused.

## Versioned file paths

The backend opens `<dir>/v<catalog version>/<file>`, not the configured `<dir>/<file>`. With `Path: /data/records.sqlite`, the file is `/data/v4/records.sqlite`. Each build keeps its own file, so two builds reading different catalog versions can run side by side, for example during a rolling deploy, without rewriting each other's files.

The first time a **durable** v4 file is opened, and an older unversioned file exists at the configured path (catalog version 2 or 3, which stored columns positionally), the backend copies it into the versioned path and renames its columns. It leaves the original untouched for builds still reading it. The copy is built under a temporary name and linked into place, so a crash leaves no half-migrated file, and two processes starting together can't overwrite each other's copy. A **derived** index is always built fresh.

## Expiry and sweeping

- A background sweeper removes every stream whose `expires_at` has passed, rows included, every `SweepInterval` (required). A failed sweep is logged and retried on the next tick.
- Before any write, an expired stream is purged, so the next append starts a new generation at seq 1 rather than colliding with stale seqs.
- `Sweep(ctx)` runs a sweep on demand and returns how many streams it removed.
- `Close()` stops the sweeper, waits for a sweep in progress, and closes the file.

## Reading the file

- `ReadDSN()`: the read-only DSN a profile connection reads the file through
- `Lease()`: holds off every row and schema mutation until released, so an external reader can run several paging statements against one stable state (see [sqlite.DB](../database/#read-leases))
- `Table(kind)`: the kind's `sqlitetable.Table`, creating it on first use

Opening the file with the `sqlite3` CLI is fine for inspection. Write to it only through the backend.
