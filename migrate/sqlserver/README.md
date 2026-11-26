# Execute a reviewed SQL Server artifact

This package applies and renders a sqlglot-go `engine.Artifact` (version 2) planned for the `sqlserver` dialect. Importing it registers that dialect with the engine. `Request`, `Result`, `Outcome` and `Apply` are the engine's own `ApplyRequest`, `Result`, `Outcome` and `Apply`.

Plan with `engine.PlanLive` (or `database/migrate.PlanSchema` for a bundle) in sqlglot-go, review the artifact, then apply it:

```go
result, err := sqlserver.Apply(ctx, db, sqlserver.Request{
    Artifact: artifact,
    Hash: reviewedHash,
    Acknowledgements: engine.Acknowledgements(artifact),
    LockTimeout: 30 * time.Second,
})
```

`Hash` must match the artifact reviewed by the caller, and `Acknowledgements` must contain each destructive change ID exactly once; a missing, unknown or duplicate ID fails before any connection is opened. `LockTimeout` is bounded to SQL Server's nonnegative millisecond range; zero makes one attempt without waiting. A forced rebuild is never acknowledged on its own: its cause is.

The artifact is split into units, each a connected set of changes. Every unit runs on its own connection under the migration application lock, in one transaction, and the engine inspects the unit's objects again before it commits, so a unit that did not reach its desired state is rolled back. A unit that already matches its desired state is skipped, which makes applying the same artifact again resume after the first failure. Earlier units stay committed and are reported. `Result.Units` lists each unit's `Outcome` and the statements it ran. The result distinguishes `applied`, `already-applied`, `failed` and `commit-unknown`; a commit error must be reconciled by inspecting the target (`engine.Check` reports each unit `applied` or `pending`, and fails a unit that matches neither state with `DriftError`) before another decision.

The fixed application-lock resource `commons-db/migrate/schema` serializes artifacts across different hashes and scopes within a database. Every cooperating migration runner must use this resource. Out-of-band schema writers must be quiesced during deployment. The application owns target identity, permissions, artifact storage and immutable attempt records.

## Post-deployment scripts

`RenderScript(artifact, ScriptOptions{Acknowledgements: ids})` renders the same reviewed artifact as one self-contained, re-runnable T-SQL batch for tools that run scripts rather than call `Apply` (for example a `PostDeployment_*` script). It validates the artifact exactly as `Apply` does and requires each destructive change acknowledged once, then emits one transaction per unit under the same `commons-db/migrate/schema` application lock, so a direct run and a script run serialise. Each unit gets a state guard evaluated before any step runs, and the script contains no `GO` line. See the sqlglot-go `migrate/ARTIFACTS.md` for the guard table.

An artifact of an earlier format (version 1, with a `request` and a `plan`) is refused: plan again.

Run the artifact seam spec with `gavel test ./compatibility/database`; it plans a table offline and renders it without a database. The engine's own execution suites live in sqlglot-go (`migrate/engine`, `database/migrate/dialect/sqlserver`) and run against a disposable SQL Server there.
