package recordresults

import (
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
)

// searchParam is the search-role param a result type with search columns takes.
const searchParam = "q"

// validateSearchColumns refuses a search column the index could not match text
// in, at registration rather than on the first search.
func validateSearchColumns(columns []query.ColumnDef, names []string) error {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			return fmt.Errorf("search column %q is named twice", name)
		}
		seen[name] = true
		index := slices.IndexFunc(columns, func(column query.ColumnDef) bool { return column.Name == name })
		if index < 0 {
			return fmt.Errorf("search column %q is not one of its columns", name)
		}
		if kind := columns[index].Type; kind != query.ColumnTypeString && kind != query.ColumnTypeJSON {
			return fmt.Errorf("search column %q is %s, and a search matches only string or json columns", name, kind)
		}
	}
	return nil
}

// searchParams is the search-role param over columns, or none without them. It
// defaults to the empty text, which matches every row, so the statement can
// bind it unconditionally: a SQL template holds only direct param references.
func searchParams(columns []string) []query.ParamDef {
	if len(columns) == 0 {
		return nil
	}
	return []query.ParamDef{{
		Name: searchParam, Label: "Search", Role: query.ParamRoleSearch, Default: "",
		Description: fmt.Sprintf("Keep the rows where %s contains this text, ignoring case", joinNames(columns)),
	}}
}

// searchClause is the predicate that keeps a row when the search is empty or
// any of columns contains it, ignoring case. instr, unlike LIKE, gives no
// character of the text a wildcard meaning.
func searchClause(table sqlitetable.Table, columns []string) (string, error) {
	if len(columns) == 0 {
		return "", nil
	}
	reference := fmt.Sprintf("{{.params.%s}}", searchParam)
	matches := []string{reference + " = ''"}
	for _, name := range columns {
		physical, err := table.Physical(name)
		if err != nil {
			return "", err
		}
		matches = append(matches, fmt.Sprintf("instr(lower(CAST(%s AS TEXT)), lower(%s)) > 0", physical, reference))
	}
	return " AND (" + strings.Join(matches, " OR ") + ")", nil
}

// joinNames lists names as prose: a, b or c.
func joinNames(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}
