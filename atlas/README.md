# atlas

Atlas extensions for commons-db, starting with a SQL Server driver that Atlas OSS does not ship.

## `internal/sqlx` is a verbatim port

`internal/sqlx` is copied verbatim from Atlas's internal package `ariga.io/atlas/sql/internal/sqlx` at **v0.38.0**, which Atlas drivers depend on but Go does not let other modules import. No file is edited: every import it needs (`ariga.io/atlas/sql/schema`, `ariga.io/atlas/sql/migrate`) is a public Atlas package. The upstream Apache-2.0 headers are kept; see `NOTICE` and `LICENSE.atlas`.

Do not edit these files. Refresh the port whenever `go.mod` bumps `ariga.io/atlas`, from the repository root:

```sh
ATLAS=$(go env GOMODCACHE)/ariga.io/atlas@$(go list -m -f '{{.Version}}' ariga.io/atlas)
cp $ATLAS/sql/internal/sqlx/sqlx.go $ATLAS/sql/internal/sqlx/sqlx_oss.go $ATLAS/sql/internal/sqlx/diff.go $ATLAS/sql/internal/sqlx/plan.go $ATLAS/sql/internal/sqlx/dev.go atlas/internal/sqlx/
cp $ATLAS/sql/internal/sqlx/sqlx_test.go $ATLAS/sql/internal/sqlx/plan_test.go $ATLAS/sql/internal/sqlx/dev_test.go atlas/internal/sqlx/
cp $ATLAS/LICENSE atlas/LICENSE.atlas
chmod u+w atlas/internal/sqlx/*.go atlas/LICENSE.atlas
go test ./atlas/internal/sqlx/...
```

Then update the version in `NOTICE` and in this README. If upstream adds a file to the package or a new import of another Atlas-internal package, port it the same way and list it in `NOTICE`.

## `internal/specutil` is a port with one edit

`internal/specutil` is copied from `ariga.io/atlas/sql/internal/specutil` at the same version: it converts Atlas HCL documents to and from `schema` values, and every driver's HCL codec is built on it. Its only edit is the import of the Atlas-internal `sqlx` package, rewritten to `internal/sqlx` above. Refresh it with `internal/sqlx`:

```sh
cp $ATLAS/sql/internal/specutil/convert.go $ATLAS/sql/internal/specutil/spec.go $ATLAS/sql/internal/specutil/convert_test.go atlas/internal/specutil/
chmod u+w atlas/internal/specutil/*.go
```

then point the `ariga.io/atlas/sql/internal/sqlx` import in `convert.go` at `github.com/flanksource/commons-db/atlas/internal/sqlx`, run `gofmt -s -w atlas/internal/specutil` and `go test ./atlas/internal/specutil/...`.

## `sqlserver` is the SQL Server driver

`atlas/sqlserver` follows the layout of `ariga.io/atlas/sql/sqlite` (driver, inspect, convert, diff, migrate, sqlspec) and registers itself with `sqlclient` as `sqlserver` (flavour `mssql`). T-SQL text -- routine definitions, data types, check, default and filter expressions -- is parsed and compared with `github.com/flanksource/sqlglot-go`. Catalog SQL comes from `internal/mssqlcatalog`, shared with the schema inspector. See the package documentation and the SQL Server section of `migrate/README.md` for what it manages and what it refuses.
