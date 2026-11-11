package query

import (
	"github.com/flanksource/clicky/api"
	dbquery "github.com/flanksource/commons-db/query"
)

func (Row) Columns() []api.ColumnDef {
	return dbquery.TableColumnsFor[Row]("profile", "provider", "time", "row")
}
func (r Row) Row() map[string]any     { return dbquery.TableRowFor(r) }
func (r Row) RowDetail() api.Textable { return r.PrettyFull() }
