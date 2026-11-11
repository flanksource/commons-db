package query

import (
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"maps"
)

type mapPresenter struct{ columns []api.ColumnDef }

func NewMapPresenter(profile Profile) (RowPresenter, error) {
	columns, err := ClickyColumns(profile)
	if err != nil {
		return nil, err
	}
	return mapPresenter{columns: columns}, nil
}

func (p mapPresenter) Columns() []api.ColumnDef { return p.columns }
func (mapPresenter) Present(row Row) (map[string]any, error) {
	values := maps.Clone(map[string]any(row))
	for key, value := range values {
		if value == nil {
			values[key] = api.TableCell{Value: api.Text{}.Append("—"), FilterValue: nil}
		}
	}
	return values, nil
}
func (mapPresenter) Detail(row Row) (api.Textable, error) { return clicky.Map(row), nil }
