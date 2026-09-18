---
title: Views
description: Named SQL profiles over a result type's streams — aggregates, groupings and derived tables served as <prefix>/<kind>/<view>.
---

A result type's own profile returns its rows. **Views** are extra SQL profiles over the same streams, served as `<prefix>/<kind>/<view>`: per-job summaries, error counts, slowest statements, and so on.

```go
recordresults.RegisterResultType(registry, recordresults.ResultType[JobEvent]{
	Kind: "job_event", Title: "Job events",
	Views: []recordresults.ResultView{
		{
			Name: "jobs", Title: "Jobs",
			Query: `SELECT job, count(*) AS events, sum(phase = 'end') AS ended,
       total(millis) AS totalMs, max(seq) AS lastSeq
FROM stream_rows GROUP BY job`,
			Columns: []query.ColumnDef{
				{Name: "job"},
				{Name: "events", Type: query.ColumnTypeNumber},
				{Name: "ended", Type: query.ColumnTypeNumber},
				{Name: "totalMs", Type: query.ColumnTypeNumber},
				{Name: "lastSeq", Type: query.ColumnTypeNumber},
			},
			Order: query.Order{{Column: "job", Unique: true}},
		},
		{
			Name: "busy_jobs", Title: "Busy jobs", Uses: []string{"jobs"},
			Params: []query.ParamDef{{Name: "minMs", Label: "Slow from", Type: query.ParamTypeNumber, Required: true}},
			Query:  `SELECT job, totalMs FROM jobs WHERE totalMs >= {{.params.minMs}}`,
			Columns: []query.ColumnDef{{Name: "job"}, {Name: "totalMs", Type: query.ColumnTypeNumber}},
			Order:   query.Order{{Column: "totalMs", Desc: true}, {Column: "job", Unique: true}},
		},
	},
})
```

## stream_rows

A view reads its stream from the CTE **`stream_rows`**: the kind's table narrowed to `{{.params.stream}}` and the `(afterSeq, toSeq]` window. The registry owns that predicate, not the view author. The index holds every environment's streams, and a view that forgot the predicate would read all of them.

Every view takes `stream`, `afterSeq` and `toSeq`, plus its own `Params`. The base profile's other params (`from`, `to`, `q`, `rootsOnly`) belong to the base profile, whose rows they bound. A view can't declare them, and a request that passes them to a view is refused, with a pointer to the base profile.

## ResultView fields

| Field | Rule |
| --- | --- |
| `Name` | a bare SQL identifier (`[A-Za-z_][A-Za-z0-9_]*`), unique ignoring case, not a SQLite keyword, and not `stream_rows` or `__cdb_base` |
| `Title` | required |
| `Uses` | names of other views of the same type, compiled in first as `AS MATERIALIZED` CTEs. Transitive uses are included once, in dependency order. Cycles are refused. |
| `Query` | a `SELECT`, optionally starting with its own `WITH …`, which is merged into the generated CTE list (`RECURSIVE` preserved) |
| `Params` | the view's own params. `identifier` params are refused. |
| `Columns` | the columns the query returns |
| `Order` | must end in a unique column, which is what pages it. Every order column must be a declared column. |

Views can also be declared in YAML. `ResultView` carries `yaml` tags (`name`, `title`, `uses`, `query`, `params`, `columns`, `order`).

## Checked when the registry opens

Each view is compiled and **read once against the index at registration**, using a stream id no real stream can have. A view that names a column the kind lacks fails when the store opens, not on its first request.

## Performance

Aggregate with `GROUP BY`. A correlated join or anti-join over a stream of 100,000 rows runs for minutes. Put shared intermediate results in a view other views `Use`, so they're materialized once per read.
