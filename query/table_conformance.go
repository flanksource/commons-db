package query

import (
	"fmt"
	"github.com/flanksource/clicky/api"
)

// ValidateTableColumns rejects presentation metadata that contradicts storage.
func ValidateTableColumns(presented []api.ColumnDef, declared []ColumnDef) error {
	columns := map[string]ColumnDef{}
	for _, column := range declared {
		columns[column.Name] = column
	}
	seen := map[string]bool{}
	for _, column := range presented {
		stored, exists := columns[column.Name]
		if !exists || seen[column.Name] {
			return fmt.Errorf("table column %q must name a unique stored column", column.Name)
		}
		seen[column.Name] = true
		if column.Unit != "" && stored.Unit != column.Unit {
			return fmt.Errorf("table column %q unit %q disagrees with stored unit %q", column.Name, column.Unit, stored.Unit)
		}
		if column.FilterKey != "" && column.FilterKey != ColumnFilterPrefix+column.Name {
			return fmt.Errorf("table column %q has invalid filter key %q", column.Name, column.FilterKey)
		}
		if column.SortKey != "" && column.SortKey != column.Name {
			return fmt.Errorf("table column %q has invalid sort key %q", column.Name, column.SortKey)
		}
		if (stored.Computed || stored.Hidden || stored.Type == ColumnTypeJSON) && (column.FilterKey != "" || column.SortKey != "") {
			return fmt.Errorf("table column %q advertises querying an unavailable scalar", column.Name)
		}
	}
	return nil
}
