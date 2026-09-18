---
title: Kinds and schemas
description: Declaring a kind's columns, key and retention in a Schemas catalog that every backend resolves through.
---

Every backend resolves a kind to its **schema** before it writes. The sqlite backend stores rows by column. Every backend needs the kind's key and retention.

## KindSchema

```go
type KindSchema struct {
	Kind    string
	Columns []query.ColumnDef
	Options KindOptions
}

type KindOptions struct {
	Key       string    // a string column identifying a row within a stream; empty = unkeyed
	Retention Retention // RetainStream (default) or RetainRows
}
```

`KindSchema.Validate` refuses a schema no backend could store:

- the kind name fails `ValidateKind`
- there are no columns, or a column fails `ColumnDef.Validate`
- the retention is unknown
- the key isn't one of the columns, or the key column isn't `query.ColumnTypeString`

## The Schemas catalog

`recordstore.Schemas` is a concurrency-safe catalog of kinds. A backend takes its method value `schemas.Kind` as a `SchemaResolver`:

```go
schemas := recordstore.NewSchemas()
err := schemas.Register("sql_deadlock", []query.ColumnDef{
	{Name: "id", Type: query.ColumnTypeString},
	{Name: "at", Type: query.ColumnTypeDateTime},
	{Name: "victim", Type: query.ColumnTypeString},
	{Name: "graph", Type: query.ColumnTypeJSON},
}, recordstore.KindOptions{Key: "id", Retention: recordstore.RetainRows})

backend, err := kv.New(kv.Options{Store: store, Prefix: "records", Schema: schemas.Kind, TTL: 30 * 24 * time.Hour, MaxChunkBytes: 1 << 20})
```

**A kind is declared once.** Registering the same kind again with identical columns and options is a no-op. Registering it with different ones is an error, because rows already written under the first declaration would become unreadable or wrongly deduplicated.

`Schemas.Kind` refuses a kind nobody declared, so a typo in a kind name fails at the first append instead of creating a stray table.

## Deriving columns from a struct

`recordresults.RegisterResultType` fills the catalog for you from a Go type, using `query.ColumnsFor(reflect.TypeFor[T]())`. The reflection reads the tags clicky already reads:

| Tag | Effect |
| --- | --- |
| `json:"name"` | column name. `json:"-"` and unexported fields are skipped, and embedded structs are flattened like `encoding/json`. `,string` stores the field as a string. |
| `pretty:"label=…"` | column label |
| `pretty:"format=…"` | display format |
| `pretty:"hide"` or `pretty:"-"` | hidden column |
| `pretty:"type=…"`, `kind=…`, `unit=…` | override the inferred `ColumnType`, set a `ColumnKind`, set a unit (`type=duration,unit=ms`) |
| `sort:"…"` | clicky's public sort key; it must equal the column name |
| `filter:"…"` | override the inferred filter: `terms`, `exact`, `text`, `range`, `duration`, `date`, `time`, `boolean`, `none`, plus `field=`, `limit=`, `options=a\|b`, `lookup=bool`, `multi=bool`, `disabled`, or `-` |

A `[]string` field becomes a JSON column that is filtered as an array. The type comes from the Go type unless `pretty:"type=…"` overrides it. **An unknown tag key or value is an error**, never ignored.

The same catalog must be shared between the backend and the registry. `recordresults.Open` does this for you. When you open backends yourself, for example the per-route backends of a `Router`, pass the same `*recordstore.Schemas` as `OpenOptions.Schemas`.

## Helpers for backend authors

| Function | Purpose |
| --- | --- |
| `ResolveKind(resolver, kind)` | resolve, then check that the schema returned names `kind` and validates |
| `KindSchema.RetentionTTL(ttl)` | the per-row TTL for a `RetainRows` kind, `0` for `RetainStream`, and an error for `RetainRows` without a TTL |
| `KindSchema.RowKeys(rows)` | each row's key, refusing missing or duplicate keys, or `nil` for an unkeyed kind |
| `Unstored(rows, keys, stored)` | keep the rows whose key `stored` doesn't report, and count the ones skipped |
| `ValidateAppend`, `ValidateStream`, `ValidateKind`, `ValidateTTL` | argument checks every backend runs before writing |
| `RefuseSealed(meta)` | the `ErrSealed` error for a sealed stream, or `nil` |
| `NewStreamMeta(stream, kind, now)` | a fresh incarnation: a new generation, `LowSeq` 1 |
