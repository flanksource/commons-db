---
title: sqlitetable
description: Write rows described by query.ColumnDef into SQLite tables a sql profile can read back under their declared column names.
---

`github.com/flanksource/commons-db/db/sqlitetable` stores rows described by `[]query.ColumnDef` in a SQLite table, and reads them back **under their declared names**. Declared names can be any string a profile can use as a column name: `elapsed ms`, `http.status`, `select`. Each one is stored under a safe physical name and aliased back on the way out.

```go
table := sqlitetable.Table{
	Name: "requests",
	Columns: []query.ColumnDef{
		{Name: "id", Type: query.ColumnTypeString},
		{Name: "at", Type: query.ColumnTypeDateTime},
		{Name: "http.status", Type: query.ColumnTypeNumber},
		{Name: "headers", Type: query.ColumnTypeJSON},
	},
	PrimaryKey: []string{"id"},
}

created, err := sqlitetable.Write(ctx, writer, table, rows) // Create + Insert
fmt.Println(created.Select())
// SELECT "id", "at", "http_status" AS "http.status", "headers" FROM "requests"
```

## Physical names

`Create` derives `StoredAs`, the physical column of each declared column, using `PhysicalNames`:

1. every run of characters outside `[A-Za-z0-9_]` becomes `_`, and the ends are trimmed
2. an empty result, or one starting with a digit, is prefixed `c_`
3. a name SQLite won't accept bare, such as a keyword, gets a trailing `_`
4. a name equal, ignoring case, to a reserved name or an earlier column is numbered `_2`, `_3`, …

`Reserved` columns keep their own names, which must already be safe. The record store reserves `stream_id` and `seq` this way.

**The derived names belong to the table.** Its owner persists `StoredAs` and hands it back on later opens, because a later build that derives differently must never reinterpret an existing table. The record store records them in its `record_kinds` catalog.

## Table methods

| Method | Does |
| --- | --- |
| `Create(ctx, db)` | creates the table, its primary key and a unique index per `Unique` column. Derives `StoredAs` when empty. The table must not exist. |
| `Derive(ctx, db)` | only derives `StoredAs`, for a table being created or migrated |
| `Insert(ctx, db, rows)` | appends rows. A row key the table doesn't declare isn't stored, and a declared column the row lacks is stored as `NULL`. |
| `Select()` | `SELECT … FROM …` with every declared column under its declared name |
| `Physical(name)` | the quoted physical column, for a `WHERE`/`ORDER BY` that addresses the table itself |

`Write(ctx, db, table, rows)` does `Create` then `Insert`, and returns the created table.

## Types

| Declared `ColumnType` | SQLite type | Stored as |
| --- | --- | --- |
| number, duration, bytes | `NUMERIC` | the number |
| boolean | `BOOLEAN` | 0/1. The driver reads it back as a Go `bool`. |
| json, keyvalue, keyvalues | `TEXT` | JSON text |
| datetime | `TEXT` | `TimeLayout` in UTC |
| everything else | `TEXT` | the value, with non-scalar values JSON-encoded |

### Time is fixed-width text

SQLite has no time type, so instants are `TEXT` compared as text. Text only orders as time when every value has the same width and zone. `TimeLayout` is `2006-01-02T15:04:05.000000000Z07:00`, always in UTC.

**Bind time bounds with `FormatTime(t)`, not a raw `time.Time`.** The driver renders a bound `time.Time` with Go's `String()`, which sorts before every stored value of its day. `RFC3339Nano` would also be wrong, because it drops trailing zeros (`…05Z` sorts after `…05.5Z`).

### Structured columns

`DecodeStructured(columns, row)` parses the JSON text of every structured column in place, so a row read back holds the value that was written. `ProfileColumns(columns)` returns the columns for a profile over the table, where each structured column reads its JSON text back as the value it encodes (`Source` = name, `JSONPath` = `$`).

`QuoteIdentifier` quotes a SQLite identifier.
