package query

func mergeInspectionColumns(discovered, supplied []ColumnDef) []ColumnDef {
	if len(supplied) == 0 {
		return discovered
	}
	columns := append([]ColumnDef(nil), discovered...)
	seen := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		seen[column.Name] = struct{}{}
	}
	for _, column := range supplied {
		if _, exists := seen[column.Name]; exists {
			continue
		}
		columns = append(columns, column)
		seen[column.Name] = struct{}{}
	}
	return columns
}
