# SQLite GORM dialector

Ported from [clarkmcc/gorm-sqlite](https://github.com/clarkmcc/gorm-sqlite/tree/00ed082c0311), retaining its MIT license. The package uses the canonical modernc SQLite engine and does not register an alternative SQL driver.

Keep this implementation internal. Consumers use the existing commons-db database APIs; they do not need a SQLite module replacement. Changes to this port must preserve migrations, constraints, error translation, and CGO-free builds.

Run `GOWORK=off go test ./compatibility/sqlite ./internal/gormsqlite` to exercise the dialector and a standalone downstream module that contains no SQLite replacement. The downstream test also verifies that a modernc-registered scalar function is available through the GORM connection.
