---
title: sqlite.DB
description: A file-backed SQLite handle with one serialized writer, a query-only read pool, read leases and buffered asynchronous writes.
---

`github.com/flanksource/commons-db/sqlite` opens a SQLite file for an application that writes and reads it at the same time. The record store's sqlite backend and the result index are built on it, and you can use it directly for any local SQLite state.

```go
import sqlitedb "github.com/flanksource/commons-db/sqlite"

db, err := sqlitedb.Open(sqlitedb.Options{
	Path:         filepath.Join(dir, "state.sqlite"),
	OnWriteError: func(err error) { logger.Errorf("async write: %v", err) }, // needed for async writes
})
defer db.Close()
```

It uses the pure-Go `modernc.org/sqlite` driver (no cgo). See [Consuming commons-db](../../start/consuming/#required-the-sqlite-driver-replace) for why that must be the only `sqlite` driver in your binary.

## Two pools over one file

| Pool | Connections | DSN | Use |
| --- | --- | --- | --- |
| writer | 1 | `journal_mode(WAL)`, `busy_timeout(5000)` | every mutation, through `Write`, `WriteAsync` and friends |
| reader | up to 10 (5 idle) | `mode=ro`, `query_only(1)`, `busy_timeout(5000)` | `db.Reader()`, and any external reader through `db.ReadDSN()` |

WAL mode lets readers run while the single writer commits. Neither pool can be used to bypass the other: the read pool is query-only at the SQLite level.

`ReadDSN()` returns the read-only `file:` URI. `recordresults` hands it to the profile engine as the index connection's URL, so profile queries can never write to the index. `Path()` returns the absolute file path.

## Writing

```go
err := db.Write(func(writer *sql.DB) error {
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs (id, started_at) VALUES (?, ?)`, id, sqlitetable.FormatTime(now)); err != nil {
		return err
	}
	return tx.Commit()
})
```

`Write` waits for exclusive access and runs the mutation immediately. After `Close` starts, it returns `ErrClosed`.

### Asynchronous writes

For fire-and-forget writes, such as telemetry, request logs or progress, queue the mutation instead:

| Method | Queues |
| --- | --- |
| `WriteAsync(mutation)` | any mutation |
| `ExecAsync(statement, args...)` | one parameterized statement |
| `InsertAsync(table, values)` | one `map[string]any` row, with columns sorted by name |
| `InsertTableAsync(table, rows)` | typed rows through a [`sqlitetable.Table`](../sqlitetable/) |

Queued mutations are collected for **100 ms** and run as one batch under the write lock. The queue holds 256 mutations. When it's full, the call returns `ErrWriteQueueFull` rather than blocking, so the caller decides whether to drop or retry.

Async writes require `Options.OnWriteError`, and fail with `ErrWriteErrorHandlerRequired` without it. A failed mutation in a batch doesn't stop the others. Each failure goes to `OnWriteError` after the write lock is released.

`Close` drains every accepted async write before closing both pools.

## Read leases

A reader that issues **several statements** that must see one stable state, such as a count and then a page, holds a lease:

```go
release := db.Lease()
defer release()

var total int
db.Reader().QueryRowContext(ctx, `SELECT count(*) FROM runs`).Scan(&total)
rows, err := db.Reader().QueryContext(ctx, `SELECT * FROM runs ORDER BY id LIMIT 100`)
```

While any lease is held, no mutation, sync or async, can start. Leases are shared, so many readers can hold one at once. The release function is idempotent. Keep leases short, because every write waits for them.

The record store exposes the same lease as `(*recordstore/sqlite.Backend).Lease()`. `recordresults` holds it across a profile read, after catching the index up.
