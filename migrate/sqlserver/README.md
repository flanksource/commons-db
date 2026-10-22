# Execute a reviewed SQL Server artifact

This package executes a sqlglot artifact using complete catalog snapshots from `inspect/sql/snapshot`. It opens one transaction, takes a transaction-owned application lock, and checks whether the database matches the baseline or already matches the complete desired state. Only a matching baseline permits SQL execution. The executor runs the exact reviewed statements, inspects the desired postcondition in the same transaction, and commits after it matches.

```go
result, err := sqlserver.Apply(ctx, db, sqlserver.Request{
    Artifact: artifact,
    Hash: reviewedHash,
    Acknowledgements: destructiveChangeIDs,
    LockTimeout: 30 * time.Second,
})
```

`Hash` must match the artifact reviewed by the caller. `Acknowledgements` must contain each destructive change ID exactly once; missing, unknown and duplicate IDs fail before the transaction opens. `LockTimeout` is bounded to SQL Server's nonnegative millisecond range; zero makes one attempt without waiting.

Module definitions execute in separate nested batches so `CREATE OR ALTER` is first in its batch. The outer dynamic batch sets `XACT_ABORT` and the required module and filtered-index session options without leaving altered settings in the pooled session. Every pre-commit failure attempts rollback. The result distinguishes `applied`, `already-applied`, `failed` and `commit-unknown`; a commit error must be reconciled by inspecting the target before another decision. `Steps` counts statements that completed before the result, including statements subsequently rolled back.

The fixed application-lock resource `commons-db/migrate/schema` serializes artifacts across different hashes and scopes within a database. Every cooperating migration runner must use this resource. Out-of-band schema writers must be quiesced during deployment. The application owns target identity, permissions, artifact storage and immutable attempt records.

Run control-flow tests with `go test ./migrate/sqlserver ./inspect/sql/snapshot -count=1`. These tests use SQL mocks and do not prove SQL Server runtime behavior.

The real engine suite is opt-in through the `integration` build tag. Set `COMMONS_DB_SQLSERVER_ARTIFACT_TEST_URL` to an admin URL for an explicitly isolated SQL Server test instance, with no database or with `database=master`, then run:

```sh
go test -tags integration ./migrate/sqlserver -run '^TestArtifactExecution$' -ginkgo.focus 'Isolated SQL Server artifacts' -count=1
```

The suite creates a uniquely named `commons_db_artifact_test_` database at compatibility level 100 per spec and drops only those databases during cleanup. It checks catalog round trips, selected changes, module batches, idempotence, data-conversion rollback, drift rejection and application-lock contention. With the integration tag enabled, missing configuration is a failure. No container is started automatically.
