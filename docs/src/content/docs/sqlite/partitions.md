---
title: Partitioned SQLite
description: Read one sqlite profile across many SQLite files that share a schema, such as one file per day or per tenant, as a single ordered, cursor-paged result.
---

A `sqlite` profile normally reads one file. With `options.partitions` it reads a set of files that share a schema and merges their rows into one result. The usual layouts are one file per day (`dt=2026-10-31/events.sqlite`), one per tenant, or a hot file plus sealed archive files.

```yaml
profile: events-by-day
provider:
  type: sqlite
  options:
    partitions:
      glob: .data/events/dt=*/events.sqlite
      labels: 'dt=(?P<dt>[0-9-]+)'
order:
  - {column: dt, desc: true}
  - {column: id, unique: true}
query: SELECT id, service FROM events
```

This example is shipped as [`profiles/events-by-day.yaml`](https://github.com/flanksource/commons-db/blob/main/profiles/events-by-day.yaml).

## Options

| Option | Meaning |
| --- | --- |
| `files` | The partition files, listed explicitly. You can't use it together with `glob`. |
| `glob` | A pattern matching the partition files. `**` matches any depth. A pattern that matches nothing is an error, not an empty result. |
| `labels` | A regular expression over each file's path. Every named group becomes a column of that file's rows. Every file must match it. |
| `immutable` | Opens every file with `immutable=1`, so SQLite skips locking. Use it only for sealed files that nothing writes to again. |
| `concurrency` | How many files are read at once. Defaults to 8. |

Every file is opened read-only. Partitions can't be combined with `connection` or `url`.

## How a page is read

The query runs in every file with the same filters and order. Each file resumes from its own last row, and the merge emits the smallest row across files until the page is full. Rows are compared in SQLite's own order: `NULL`, then numbers, then text, then blobs. Integers compare exactly, and nulls sort last in both directions, the same as a single-file read.

- **Row reads page by cursor only.** An offset into a merge would mean reading and discarding every earlier row in every file, so asking for one is an error. Aggregates page either way (see [Aggregates](#aggregates)).
- **The `order` must end in a `unique` column**, as for any paged profile. That column only needs to be unique within one file: rows that tie across files are ordered by file path, and the cursor carries each file's own position, so a resumed walk neither repeats nor skips them.
- **The first page's total** is the sum of the files' totals.

## Labels and pruning

A label is a constant column: `dt` above is in every row and can be filtered, ordered and selected like a stored column. An exact-value filter on a label, such as `dt=2026-10-30` or its exclusion, skips the files it rules out before opening them. Every other filter runs in SQL in every file. That is always correct, but each file is opened.

If a file lacks a column the query uses, the read fails with an error naming that file.

## Aggregates

A query whose rows are groups can't simply be merged: a group with rows in three files would come back three times. Write the query as you would for one file, and the read plans it in one of two ways.

```yaml
query: SELECT service, count(*) AS n, avg(bytes) AS mean FROM events GROUP BY service
```

- **Combined:** used when every aggregate is `count`, `sum`, `total`, `min`, `max` or `avg`, with an optional `GROUP BY` and `HAVING`. Each file computes partial results for its own groups: `avg` becomes a sum and a count. The final query then groups those partials again and combines them (counts are summed, `avg` is the summed sum over the summed count). Only one row per group per file leaves each file.
- **Gathered:** used for anything else, such as `count(DISTINCT …)`, `percentile`, `group_concat`, `DISTINCT` or a window function. Each file's rows that pass the `WHERE` are copied into a scratch SQLite file under `.tmp/`, and the query runs once over all of them. The scratch file is deleted when the read ends. This needs the query to read a single unqualified table.

Either way the final query runs in one file, so it pages by offset as well as by cursor, and filters and `order` apply to its result columns as usual. Labels can be grouped on like stored columns.

Some queries are refused with an error naming the problem:

- an aggregate inside a `UNION`
- a CTE or subquery: it would run inside one file and see only that file's rows
- an unaliased computed column, such as `count(*)` without `AS n`, because the combined query computes it with a different expression and SQLite would name it differently
- a column that is neither grouped nor aggregated

## Not yet supported

- `median(...)` fails to render in the sqlite dialect, for single-file profiles as well. Use `percentile(x, 50)` until it's fixed.
- Filter value lookups, column inspection and capability probes refuse partitioned profiles.
