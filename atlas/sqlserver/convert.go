package sqlserver

import (
	"ariga.io/atlas/sql/schema"
	"github.com/flanksource/commons-db/internal/mssqlcatalog"
	planner "github.com/flanksource/sqlglot-go/migrate/atlas/sqlserver"
)

func CatalogType(c mssqlcatalog.Column) (schema.Type, error) {
	return planner.CatalogType(planner.CatalogColumn{Name: c.Name, DataType: c.DataType, MaxLength: c.MaxLength, Precision: c.Precision, Scale: c.Scale})
}
