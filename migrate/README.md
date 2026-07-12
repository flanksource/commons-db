# HCL migrations

`migrate.Apply` reconciles PostgreSQL tables with Atlas HCL and runs colocated
SQL migrations. Every bundle should use a stable `WithName` when more than one
bundle can share a database.

SQL files default to the post-Atlas phase. Directives are read from the leading
comment header:

```sql
-- phase: pre
-- dependsOn: 001_extensions.sql
-- runs: always
-- transaction: false
```

- `phase` is `pre` or `post`; omitted means `post`.
- `dependsOn` uses paths relative to the migration root. Dependencies are
  topologically ordered and changes rerun all transitive dependents.
- scripts are transactional by default; the SQL and its SHA-256 migration log
  commit atomically.

Atlas-style PostgreSQL `role` and `permission` blocks may live in the same HCL
files as tables. The OSS migration layer supports roles and memberships plus
schema, table, column, and sequence permissions. Managed grants are reconciled
exactly within the bundle's metadata scope; unrelated roles and grants are left
untouched. Different scopes must not manage the same role or grantee/object pair.
