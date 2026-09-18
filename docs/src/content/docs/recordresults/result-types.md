---
title: Result types
description: Declare a Go type as a result type — its kind, time window, key, retention, search, hierarchy, follow and presentation.
---

A result type declares one kind of record a stream holds, and the profile that serves it.

```go
func RegisterResultType[T any](registry *Registry, resultType ResultType[T]) error
```

`T`'s columns come from `query.ColumnsFor` (see [Kinds and schemas](../../recordstore/kinds/#deriving-columns-from-a-struct)). They become the kind's schema in the shared catalog, the index table `records_<kind>`, and the columns of the profile `<prefix>/<kind>`.

## ResultType fields

| Field | Effect |
| --- | --- |
| `Kind` | the stream kind, and the last segment of the profile name. Required. |
| `Title` | human name. Required. |
| `TimeColumn` | a `time.Time` field (a `datetime` column). It becomes the table's timestamp, rows are ordered newest first, and the profile takes a `from`/`to` window over it. Without it, rows are in seq order and there's no time window. |
| `DefaultFrom` | where the window starts when a request names no `from`: date math (`now-12h`) or RFC3339. Checked at registration. Needs `TimeColumn`. |
| `KeyColumn` | a string field that identifies a row within a stream. A stream holds each key once (`KindOptions.Key`). |
| `Retention` | `recordstore.RetainStream` (default) or `RetainRows`. See [Concepts](../../recordstore/concepts/#retention). |
| `Follow` | lets a session tail the type's streams. See [below](#follow). |
| `SearchColumns` | string or JSON columns a search matches. Adds a `q` param. |
| `Hierarchy` | `&HierarchyColumns{ID, Parent}` for root-only reads. Adds a `rootsOnly` param. |
| `Views` | SQL profiles over the type's streams. See [Views](../views/). |

Registration fails fast when:

- a named column doesn't exist or has the wrong type
- the type declares `seq` or `stream_id`, which every stream table reserves
- the kind is already registered
- `Follow` is set but the registry's `Source` isn't a `*recordstore.Notifier`

## Profile params

Every result profile takes:

| Param | Type | Default | Meaning |
| --- | --- | --- | --- |
| `stream` | string | — (required) | the record stream to read |
| `afterSeq` | number | `0` | read rows after this seq |
| `toSeq` | number | max int64 | read rows up to and including this seq |
| `from` | datetime | `DefaultFrom` or open | with `TimeColumn`: rows at or after this time |
| `to` | datetime | open | with `TimeColumn`: rows before this time |
| `q` | string | `""` | with `SearchColumns`: rows where any search column contains the text, ignoring case |
| `rootsOnly` | boolean | `false` | with `Hierarchy`: only rows whose parent is not recorded in this stream |

On top of these, the engine's own column filters (`filter.<column>=…`), sort, paging (`limit`, `cursor`) and export (`scope=all`, `format=csv|json|ndjson|yaml|markdown|html|excel|pdf`) apply. Pages default to 100 rows (500 max). A whole-stream export stops at `MaxExportRows` (1,000,000).

`seq` is always the last, unique order column, which is what lets the engine page past the first page. `stream_id` is a hidden column.

## Search

```go
recordresults.ResultType[QueryEvent]{
	Kind: "query_event", Title: "Query events",
	SearchColumns: []string{"statement", "user"},
}
```

The search uses `instr(lower(col), lower(q))`, so no character in the text has a wildcard meaning, unlike `LIKE`. An empty `q` matches every row.

## Hierarchy

For invocation trees, such as spans or nested calls, where each row names its caller:

```go
recordresults.ResultType[Invocation]{
	Kind: "invocation", Title: "Invocations",
	KeyColumn: "id",
	Hierarchy: &recordresults.HierarchyColumns{ID: "id", Parent: "parent_id"},
}
```

`ID` must be the `KeyColumn`, and both columns must be strings. With `rootsOnly=true`, a row is kept when its parent is empty, or when the parent's id isn't recorded in the same stream. That covers a partial capture whose callers were never seen.

## Follow

A type with `Follow: true` reads through the `recordstore` provider instead of plain `sqlite`. It pages, filters, looks values up and exports exactly as the sqlite provider does, and it can also **stream**: a session started with `follow=true` emits every row of the stream after `afterSeq`, then each row as it's appended. It ends at an explicit `toSeq`, at the end of a sealed stream, or when the stream stops existing.

A followed row is exactly the row a page of the same seqs serves, because the follow runs the profile's own statement and column filters in reads of up to 500 rows, each holding the index lease briefly. Only follow a type whose rows each stand alone. A type listed as "the latest row per id" would stream superseded rows.

Rows appended through `results.Backend` arrive immediately. The follow re-checks that its stream still exists every `FollowRecheckInterval` (5s).

## Presentation: TableProvider

When `T` (or `*T`) implements clicky's `api.TableProvider`, the registry uses it as the profile's row presenter. Each indexed row is decoded back into `T`, `Row()` renders it, and `Columns()` defines the presented columns, with `seq` prepended:

```go
func (QueryEvent) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("at").Label("Captured").Build(),
		api.Column("statement").Label("Statement").Build(),
	}
}

func (e QueryEvent) Row() map[string]any {
	return map[string]any{"at": e.At, "statement": e.Statement}
}
```

Types that don't implement it are presented from the declared columns.

## Listing registered types

`registry.ResultTypes()` returns every registered type by kind, with its profile name and views. A UI can use it to build navigation.

```json
[{ "kind": "job_event", "title": "Job events", "profile": "traces/job_event",
   "views": [{ "name": "jobs", "title": "Jobs", "profile": "traces/job_event/jobs" }] }]
```
