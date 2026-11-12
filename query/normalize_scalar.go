package query

import (
	"fmt"
	"strconv"
)

func normalizeBooleanColumns(columns []ColumnDef, row Row) error {
	for _, column := range columns {
		if column.Type != ColumnTypeBoolean || column.CEL != "" || column.JSONPath != "" {
			continue
		}
		name := column.Name
		if column.Source != "" {
			name = column.Source
		}
		value := row[name]
		if value == nil {
			continue
		}
		if _, ok := value.(bool); ok {
			continue
		}
		text := fmt.Sprint(value)
		parsed, err := strconv.ParseBool(text)
		if err != nil {
			return fmt.Errorf("column %q: expected boolean or SQL 0/1, received %T(%v)", column.Name, value, value)
		}
		row[name] = parsed
	}
	return nil
}
