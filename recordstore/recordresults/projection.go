package recordresults

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/commons-db/query"
)

// ProjectJSON serves captured, already mapped profile values with their original
// column grammar. The outer query lets native filters run before pagination.
func ProjectJSON(profile query.Profile, field string, columns []query.ColumnDef, order query.Order) (query.Profile, error) {
	if !slices.ContainsFunc(profile.Columns, func(c query.ColumnDef) bool { return c.Name == field && c.Type == query.ColumnTypeJSON }) {
		return query.Profile{}, fmt.Errorf("projection column %q is not a stored JSON column", field)
	}
	quote := func(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
	selects := []string{`"seq"`}
	projected := slices.Clone(columns)
	for i, column := range projected {
		if column.Name == "seq" || column.Name == "" {
			return query.Profile{}, fmt.Errorf("invalid captured column %q", column.Name)
		}
		path, err := json.Marshal(column.Name)
		if err != nil {
			return query.Profile{}, err
		}
		selects = append(selects, fmt.Sprintf("json_extract(%s, '%s') AS %s", quote(field), strings.ReplaceAll("$."+string(path), "'", "''"), quote(column.Name)))
		projected[i].Source, projected[i].CEL, projected[i].JSONPath = "", "", ""
		projected[i].Computed = false
		if column.Filter != nil {
			filter := *column.Filter
			filter.Field = column.Name
			projected[i].Filter = &filter
		}
	}
	profile.Query = "SELECT " + strings.Join(selects, ", ") + " FROM (" + profile.Query + ")"
	profile.Columns = append(projected, query.ColumnDef{Name: "seq", Type: query.ColumnTypeNumber, Hidden: true})
	profile.Order = append(slices.Clone(order), query.OrderBy{Column: "seq", Unique: true})
	var err error
	profile.Presenter, err = query.NewMapPresenter(profile)
	return profile, err
}
