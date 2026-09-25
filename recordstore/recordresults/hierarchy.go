package recordresults

import (
	"fmt"
	"slices"

	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
)

func validateHierarchy(columns []query.ColumnDef, key string, hierarchy *HierarchyColumns) error {
	if hierarchy == nil {
		return nil
	}
	if hierarchy.ID == hierarchy.Parent {
		return fmt.Errorf("hierarchy id and parent columns must differ, got %q", hierarchy.ID)
	}
	if key != hierarchy.ID {
		return fmt.Errorf("hierarchy id column %q must be the result key column, got %q", hierarchy.ID, key)
	}
	for _, name := range []string{hierarchy.ID, hierarchy.Parent} {
		index := slices.IndexFunc(columns, func(column query.ColumnDef) bool { return column.Name == name })
		if index < 0 {
			return fmt.Errorf("hierarchy column %q is not one of its columns", name)
		}
		if columns[index].Type != query.ColumnTypeString {
			return fmt.Errorf("hierarchy column %q is %s, not a string", name, columns[index].Type)
		}
	}
	return nil
}

func hierarchyParams(hierarchy *HierarchyColumns) []query.ParamDef {
	if hierarchy == nil {
		return nil
	}
	return []query.ParamDef{{
		Name: rootsOnlyParam, Label: "Roots only", Type: query.ParamTypeBoolean, Default: false,
		Description: "Show only rows whose parent is not recorded in this stream",
	}}
}

func hierarchyClause(table sqlitetable.Table, hierarchy *HierarchyColumns) (string, error) {
	if hierarchy == nil {
		return "", nil
	}
	stream, err := table.Physical(streamIDKey)
	if err != nil {
		return "", err
	}
	id, err := table.Physical(hierarchy.ID)
	if err != nil {
		return "", err
	}
	parent, err := table.Physical(hierarchy.Parent)
	if err != nil {
		return "", err
	}
	outer := sqlitetable.QuoteIdentifier(table.Name)
	return fmt.Sprintf(` AND ({{.params.%s}} = 0 OR %s.%s IS NULL OR %s.%s = '' OR `+
		`NOT EXISTS (SELECT 1 FROM %s AS caller WHERE caller.%s = %s.%s AND caller.%s = %s.%s))`,
		rootsOnlyParam, outer, parent, outer, parent, outer, stream, outer, stream, id, outer, parent), nil
}
