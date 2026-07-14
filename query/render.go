package query

import (
	"fmt"
	"sort"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
)

// rowProvider adapts a single Row plus a shared column schema to clicky's
// api.TableProvider so the Result can be rendered through every clicky
// formatter (table, csv, json, xlsx, html-react, ...).
type rowProvider struct {
	cols []api.ColumnDef
	row  Row
}

func (r rowProvider) Columns() []api.ColumnDef { return r.cols }
func (r rowProvider) Row() map[string]any      { return r.row }

// Table builds a clicky TextTable from the Result. When columns is empty, the
// columns are derived from the union of row keys (sorted for determinism).
func (r *Result) Table(columns []ColumnDef) api.TextTable {
	cols := clickyColumns(columns, r.Rows)
	if len(r.Rows) == 0 {
		return emptyTable(cols)
	}

	providers := make([]rowProvider, len(r.Rows))
	for i, row := range r.Rows {
		providers[i] = rowProvider{cols: cols, row: row}
	}
	return api.NewTableFrom(providers)
}

// Render formats the Result in the given clicky format (e.g. "table", "csv",
// "json", "xlsx", "html").
func (r *Result) Render(columns []ColumnDef, format string) (string, error) {
	return clicky.Format(r.Table(columns), clicky.FormatOptions{Format: format})
}

// ClickyColumns maps declared profile columns to the shared Clicky column
// contract. Schema-less callers may pass nil and let a formatter derive fields.
func ClickyColumns(columns []ColumnDef) []api.ColumnDef {
	return clickyColumns(columns, nil)
}

// clickyColumns maps profile columns to clicky column definitions, deriving the
// schema from row keys when no columns are declared.
func clickyColumns(columns []ColumnDef, rows []Row) []api.ColumnDef {
	if len(columns) == 0 {
		columns = deriveColumns(rows)
	}

	out := make([]api.ColumnDef, 0, len(columns))
	for _, c := range columns {
		out = append(out, api.ColumnDef{
			Name:     c.Name,
			Label:    c.Label,
			Kind:     string(c.Kind),
			Type:     string(c.Type),
			Format:   c.clickyFormat(),
			MaxWidth: c.Width,
			Hidden:   c.Hidden,
		})
	}
	return out
}

// emptyTable builds a header-only TextTable so empty result sets still render
// their column chrome (clicky's NewTableFrom drops headers when there are no
// rows).
func emptyTable(cols []api.ColumnDef) api.TextTable {
	t := api.TextTable{}
	for _, col := range cols {
		if col.Hidden {
			continue
		}
		style := col.Style
		if col.MaxWidth > 0 {
			style = fmt.Sprintf("%s max-w-[%dch] truncate", style, col.MaxWidth)
		}
		t.Headers = append(t.Headers, api.Text{Content: col.DisplayLabel(), Style: col.HeaderStyle})
		t.FieldNames = append(t.FieldNames, col.Name)
		t.Columns = append(t.Columns, api.PrettyField{
			Name:          col.Name,
			Label:         col.DisplayLabel(),
			Kind:          col.Kind,
			Style:         style,
			LabelStyle:    col.HeaderStyle,
			Type:          col.Type,
			Format:        col.Format,
			FormatOptions: col.FormatOptions,
		})
	}
	return t
}

// deriveColumns builds a stable, sorted column list from the union of row keys.
func deriveColumns(rows []Row) []ColumnDef {
	seen := map[string]bool{}
	var names []string
	for _, row := range rows {
		for k := range row {
			if !seen[k] {
				seen[k] = true
				names = append(names, k)
			}
		}
	}
	sort.Strings(names)

	cols := make([]ColumnDef, len(names))
	for i, n := range names {
		cols[i] = ColumnDef{Name: n}
	}
	return cols
}
