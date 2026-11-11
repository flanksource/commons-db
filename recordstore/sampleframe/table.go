package sampleframe

import (
	"fmt"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/commons-db/query"
)

func (f Frame) Pretty() api.Text {
	return api.Text{}.Append(f.Source, "text-blue-500").Space().Add(api.Human(f.CompletedAt)).Space().Append(fmt.Sprintf("%d/%d rows", f.Retained, f.Observed), "text-muted")
}
func (f Frame) PrettyFull() api.Textable {
	return f.Pretty().NewLine().Add(clicky.Map(query.TableRowFor(f)))
}
func (Frame) Columns() []api.ColumnDef {
	return query.TableColumnsFor[Frame]("source", "completedAt", "retained", "observed", "limit")
}
func (f Frame) Row() map[string]any     { return query.TableRowFor(f) }
func (f Frame) RowDetail() api.Textable { return f.PrettyFull() }
