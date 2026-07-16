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
- `runs: always` re-runs a script on **every** apply, including a plain process
  boot against an unchanged database. It is an **escape hatch**, not the norm.
  Normal scripts are hash-gated: they run once and re-run only when their content
  changes. Views are restored automatically when Atlas reshapes a base table they
  depend on — `migrate.Apply` drops the dependent view and re-runs the script
  that creates it — so a view file never needs `runs: always` to stay current.
  Reserving `always` for the rare script that must reconcile on every apply keeps
  steady-state boots DDL-free; a bundle full of `always` scripts re-issues
  `ACCESS EXCLUSIVE`-locking DDL on every connection and will deadlock live
  readers. Prefer splitting DDL into small hash-gated files — one file per view
  (or per shared dependency), triggers and constraints in their own run-once
  files — so a table reshape only re-creates the views that actually depend on
  it and never disturbs unrelated triggers or constraints.

Atlas-style PostgreSQL `role` and `permission` blocks may live in the same HCL
files as tables. The OSS migration layer supports roles and memberships plus
schema, table, column, and sequence permissions. Managed grants are reconciled
exactly within the bundle's metadata scope; unrelated roles and grants are left
untouched. Different scopes must not manage the same role or grantee/object pair.
