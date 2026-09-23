# HCL migrations

`migrate.Apply` is the single filesystem-backed migration seam for PostgreSQL and SQLite. The connection string selects the target, while callers pass the same embedded Atlas HCL bundle and options.

```go
err := migrate.Apply(ctx, dsn, schemaFS,
    migrate.WithDir("migrations"),
    migrate.WithName("runtime"),
)
```

## Target selection

PostgreSQL accepts `postgres://`, `postgresql://`, and keyword connection strings such as `host=localhost dbname=runtime sslmode=disable`.

SQLite accepts `sqlite://` URLs and plain file paths ending in `.db`:

```go
err := migrate.Apply(ctx, "sqlite://state/runtime.sqlite", schemaFS, migrate.WithDir("migrations"))
err := migrate.Apply(ctx, "/var/lib/example/runtime.db", schemaFS, migrate.WithDir("migrations"))
```

Relative SQLite paths are resolved from the process working directory. SQLite is always file-backed; `:memory:` and `mode=memory` are rejected. The normalized connection enables foreign keys, a 5-second busy timeout, and WAL while preserving caller-supplied query parameters and unmanaged pragmas.

Unknown URI schemes written as `scheme://...` fail instead of being passed to PostgreSQL accidentally. Scheme-like strings without `://` are treated as PostgreSQL DSNs unless their path ends in `.db`; use one of the explicit forms above rather than relying on that ambiguity. A PostgreSQL keyword DSN remains PostgreSQL even when its `dbname` ends in `.db`.

`db.NewGorm` uses the same DSN resolver, so migration and application connections cannot disagree about the target.

## Portable canonical HCL

A cross-database bundle declares PostgreSQL-shaped Atlas HCL. PostgreSQL evaluates the declaration directly. SQLite evaluates the same declaration with the PostgreSQL evaluator, validates the resulting realm against the compatibility contract below, and then projects it to SQLite. Acceptance by the HCL parser alone does not mean an object is SQLite-compatible.

All `.hcl` files below `WithDir` are loaded recursively in lexical path order and evaluated as one realm. `variable` blocks are supported on both targets when callers provide values through `WithVariables`; the resulting schema must still fit the portable set.

### Supported cross-database blocks

| HCL block | Supported portable form | SQLite behavior |
| --- | --- | --- |
| `variable` | Atlas variable declarations referenced by schema expressions | Evaluated before projection using `WithVariables` values |
| `schema` | Exactly one `schema "public" {}` | Projected to SQLite's `main` schema |
| `table` | One or more tables assigned to `schema.public` | Created with their declared portable columns and constraints |
| `column` | `type`, `null`, and a literal or `sql(...)` default | Type is mapped as documented below; default SQL is preserved verbatim |
| `primary_key` | Column references only | Preserved when the table is first created or safely rebuilt |
| `unique` | Named constraint over column references | Projected to a named unique index |
| `index` | Column references, optional `unique`, and optional `where`; default or explicit B-tree method only | Projected to a SQLite index; `where` is preserved verbatim |
| `foreign_key` | Local and referenced columns plus `on_update` and `on_delete` actions | Preserved when the table is first created or safely rebuilt |
| `check` | Named check with `expr` and no dialect attributes | Preserved verbatim when the table is first created or safely rebuilt |

Index expressions, per-column index attributes such as sort direction or operator classes, foreign-key attributes, and table or check attributes outside the forms above are not in the portable set.

### Supported portable column types

The following table lists the PostgreSQL type families accepted after HCL evaluation, not every type accepted by Atlas's PostgreSQL parser. Underscored names such as `character_varying` and `double_precision` are direct HCL aliases. Space-containing SQL spellings and the less common aliases noted below are accepted through `sql("...")` and normalize to the same family.

| PostgreSQL HCL type | SQLite declaration | Compatibility notes |
| --- | --- | --- |
| `uuid` | `text` | No SQLite UUID-format constraint is added |
| `text`, `char`, `character`, `varchar`, `character_varying`, `bpchar`, `name`; SQL spelling `character varying` | `text` | Length modifiers and PostgreSQL's internal `name` limit are not preserved |
| `json`, `jsonb` | `text` plus `json_valid(column)` | The generated check is named `<table>_<column>_json` |
| `date`, `time`, `timetz`, `timestamp`, `timestamptz`; SQL spellings `time with time zone`, `time without time zone`, `timestamp with time zone`, `timestamp without time zone` | `datetime` | Precision and timezone enforcement are not preserved by SQLite |
| `int2`, `int4`, `int8`, `int`, `integer`, `smallint`, `bigint`, `xid`, `xid8` | `integer` | PostgreSQL width and transaction-ID distinctions collapse to SQLite integer affinity |
| `bool`, `boolean` | `bool` | SQLite gives the declared `bool` type NUMERIC affinity; it has no native boolean type |
| `bytea` | `blob` | Binary values remain binary |
| `numeric`, `decimal` | `numeric` | Precision and scale modifiers are not preserved |
| `real`, `float`, `float4`, `float8`, `double_precision` | `real` | Precision modifiers and PostgreSQL width distinctions are not preserved |

Use only type modifiers whose loss is acceptable to the application, or enforce the invariant with a portable named `check`. Evaluated type families outside this table fail projection; they are not coerced to text.

### Portable example

```hcl
schema "public" {}

table "projects" {
  schema = schema.public

  column "id" {
    type = uuid
    null = false
  }
  column "payload" {
    type    = jsonb
    null    = false
    default = sql("'{}'")
  }
  column "created_at" {
    type    = timestamptz
    null    = false
    default = sql("CURRENT_TIMESTAMP")
  }

  primary_key {
    columns = [column.id]
  }
}

table "events" {
  schema = schema.public

  column "id" {
    type = uuid
    null = false
  }
  column "project_id" {
    type = uuid
    null = false
  }
  column "event_key" {
    type = text
    null = false
  }
  column "active" {
    type    = bool
    null    = false
    default = false
  }

  primary_key {
    columns = [column.id]
  }
  foreign_key "events_project_id_fkey" {
    columns     = [column.project_id]
    ref_columns = [table.projects.column.id]
    on_update   = NO_ACTION
    on_delete   = CASCADE
  }
  index "events_active_key" {
    unique  = true
    columns = [column.project_id, column.event_key]
    where   = "active"
  }
  check "events_event_key_nonempty" {
    expr = "length(event_key) > 0"
  }
}
```

Defaults, checks, and partial-index predicates are SQL strings, not a portable expression language. `commons-db` does not translate them. Every expression in a cross-database bundle must be accepted with the intended meaning by both PostgreSQL and SQLite. `CURRENT_TIMESTAMP` and `sql("'{}'")` are portable examples; PostgreSQL-only casts such as `::jsonb`, functions such as `now()`, operators, and JSON expressions must not be assumed portable.

### SQLite rejection list

SQLite rejects the following before schema mutation:

| Incompatible declaration or option | Reason |
| --- | --- |
| No table declarations | The SQLite reconciler requires at least one declared table |
| More than one schema, or a schema other than `public` | A portable bundle maps exactly one canonical schema to SQLite `main` |
| Realm or schema attributes and objects, including enums, domains, extensions, sequences, views, functions, and procedures | There is no defined lossless projection |
| Table triggers | Trigger SQL is dialect-specific |
| Column types outside the supported type table | The projector never guesses an affinity |
| Column attributes such as generated or identity behavior | PostgreSQL attributes cannot be preserved by the current projection |
| Table attributes other than plain named checks | Storage parameters, partitioning, and other PostgreSQL metadata are not portable |
| Check attributes | Only the check name and expression are portable |
| Non-B-tree index methods | SQLite cannot preserve PostgreSQL access-method behavior |
| Expression indexes or index-part attributes | Only direct column references are portable |
| Foreign-key attributes beyond columns, references, and actions | PostgreSQL-only metadata would be lost |
| A user check named `<table>_<json-column>_json` with a different expression | That name is reserved for the generated `json_valid` check |
| Any `.sql` migration file | SQL phases are PostgreSQL-only; SQLite does not skip them silently |
| `role` or `permission` blocks | Managed security is PostgreSQL-only |
| `WithExclude(...)` | Partial inspection could hide SQLite drift |
| `WithSchema(...)` with a value other than `public` | SQLite has no PostgreSQL schema namespace |
| `WithDrops()` | The SQLite path is intentionally additive-only |

These declarations are parser-valid PostgreSQL HCL but are not SQLite-compatible: bit strings, network and MAC types, spatial types, intervals, serial and identity types, XML, money, arrays, ranges and multiranges, full-text search types, enums, domains, and other user-defined types.

### Accepted declarations with different SQLite semantics

Some declarations are accepted because their storage representation is useful on both targets, but SQLite cannot enforce every PostgreSQL semantic:

- UUIDs are stored as text without a canonical-format check.
- String length, numeric precision and scale, floating-point width, and time precision are normalized to SQLite affinities.
- `json` and `jsonb` both become validated JSON text; PostgreSQL JSONB indexing and operators are not provided.
- Date and time families become `datetime`; SQLite does not enforce PostgreSQL timezone or temporal-type distinctions.
- Integer widths collapse to SQLite integer affinity.
- Boolean declarations have NUMERIC affinity and do not restrict stored values to `0` or `1`; add a portable check when that invariant matters.
- Defaults, checks, foreign-key actions, and partial-index predicates are carried as written. Syntax or semantics that differ between engines fail at apply time or behave according to the target engine.

### SQLite migration-time limits

The SQLite apply runs in one transaction and is additive by default. A newly created table may contain every supported constraint above. For an existing table, only missing columns and indexes are applied unless the caller opts in with `WithRebuilds()`.

`WithRebuilds()` allows Atlas to copy existing rows through a transactional SQLite table rebuild for added primary keys, foreign keys, and checks, plus column default and nullability changes. Added indexes are also supported. It disables foreign-key enforcement before opening the transaction, verifies that no new foreign-key violations appear before commit, and restores enforcement afterward. An incompatible value or constraint rolls the transaction back. Drops, renames, index modifications, and column type changes remain refused; in particular, an existing unique index cannot be removed by opting into rebuilds. A table with an unmanaged trigger is refused rather than silently losing that trigger during rebuild. PostgreSQL accepts the option but already applies these schema changes through its ordinary migration path.

Without `WithRebuilds()`, adding a check or foreign key to an existing table is unsupported, and adding a JSON column may be refused because its generated `json_valid` check is a table constraint. An otherwise portable added column must also satisfy SQLite's native `ALTER TABLE ADD COLUMN` restrictions; for example, a required column generally needs a usable default when rows already exist. `WithRebuilds()` does not transform or backfill values. Tables and views outside the bundle are not managed; callers must assess views that depend on changed columns before applying a rebuild.

## PostgreSQL schema-scoped bundles

`WithSchema` applies one bundle inside a normalized PostgreSQL schema and leaves the default behavior on `public` unchanged:

```go
err := migrate.Apply(ctx, dsn, schemaFS,
    migrate.WithName("runtime"),
    migrate.WithSchema("agent_tenant_context"),
)
```

The schema name must be a lowercase PostgreSQL identifier no longer than 63 bytes. The migration creates the schema and scopes Atlas inspection, HCL objects, SQL migration metadata, dependent-view handling, and managed security to it. A reusable HCL bundle still declares only `schema "public" {}`; migration remaps that declaration to the selected schema.

SQL files run with the selected schema as `search_path`. Keep objects owned by the bundle unqualified so the same bundle can target different schemas. Explicitly qualified SQL remains explicit and is not rewritten.

## PostgreSQL SQL phases and security

SQL files default to the post-Atlas phase. Directives are read from the leading comment header:

```sql
-- phase: pre
-- dependsOn: 001_extensions.sql
-- runs: always
-- transaction: false
```

- `phase` is `pre` or `post`; omitted means `post`.
- `dependsOn` uses paths relative to the migration root. Dependencies are topologically ordered and changes rerun all transitive dependents.
- Scripts are transactional by default; the SQL and its SHA-256 migration log commit atomically.
- `runs: always` re-runs a script on every apply. It is an escape hatch for reconciliation that truly must occur at every boot, not a replacement for dependency tracking.

Views are restored automatically when Atlas reshapes a base table they depend on. Prefer small hash-gated files, such as one file per view or shared dependency, so a table reshape re-creates only affected objects and steady-state startup remains DDL-free.

Atlas-style PostgreSQL `role` and `permission` blocks may live in the same HCL files as tables. The OSS migration layer supports roles and memberships plus schema, table, column, and sequence permissions. Managed grants are reconciled exactly within the bundle's metadata scope; unrelated roles and grants remain untouched. Different scopes must not manage the same role or grantee/object pair.

## Reusable PostgreSQL test templates

`migrate.NewProvisioner` adapts an HCL and SQL bundle to `dbtest.Options.Provisioner`:

```go
handle := dbtest.ForT(t, dbtest.Options{
    Name:    "query_sessions",
    LogName: "query-sessions-test",
    Provisioner: migrate.NewProvisioner(
        querySchema,
        migrate.WithDir("migrations"),
        migrate.WithName("query"),
    ),
})
```

The provisioner fingerprints the migration name, directory, Atlas options, variables, and every HCL and SQL file. `dbtest` prepares one sealed PostgreSQL template for that fingerprint, clones an isolated database for each call, and keeps the template for later tests. Normal hash-gated SQL is already present in the clone; `runs: always` scripts and security reconciliation still run for every instance.

This option requires database creation. `dbtest` fails before invoking the provisioner when `COMMONS_DB_CREATE=false` because that mode targets the configured database directly.

## Programmatic SQLite declarations

The `migrate/sqlite` subpackage remains the lower-level seam for callers that already own a `*sql.DB` or `*sql.Tx` and construct Atlas `*schema.Table` declarations in Go. Its `ReconcileTables` function uses the same additive-only policy but does not load or project HCL.

```go
declared, err := sqlitetable.Table{Name: "events", Columns: columns, StoredAs: stored, PrimaryKey: []string{"id"}}.Declare()
if err != nil {
    return err
}
if err := sqlitemigrate.ReconcileTables(ctx, tx, declared); err != nil {
    return err
}
```

The record store uses this lower-level API to add newly declared columns to tables that may already contain rows. New filesystem-backed migrations should call the target-aware parent `migrate.Apply` instead.
