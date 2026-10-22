// Package sqlserver preserves the commons-db API; implementations live in sqlglot-go.
package sqlserver

import native "github.com/flanksource/sqlglot-go/database/atlas/sqlserver"
import "ariga.io/atlas/sql/schema"
import "github.com/flanksource/commons-db/internal/mssqlcatalog"

func CatalogType(c mssqlcatalog.Column) (schema.Type, error) { return native.CatalogType(c) }
