package db

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

var postgresTypes = pgtype.NewMap()

// ScanRows scans all rows of a *sql.Rows into a slice of map-like records,
// keyed by column name. Ported from duty/db so SQL data providers can return
// generic rows without a typed model.
func ScanRows[T ~map[string]any](rows *sql.Rows) ([]T, error) {
	columnTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, fmt.Errorf("failed to get column types: %w", err)
	}

	columnNames := make([]string, len(columnTypes))
	for i, columnType := range columnTypes {
		columnNames[i] = columnType.Name()
	}

	values := make([]any, len(columnNames))
	valuePtrs := make([]any, len(columnNames))
	for i := range values {
		valuePtrs[i] = &values[i]
	}

	var result []T
	for rows.Next() {
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		row := make(T, len(columnNames))
		for i, column := range columnNames {
			value, err := normalizeSQLValue(columnTypes[i].DatabaseTypeName(), values[i])
			if err != nil {
				return nil, fmt.Errorf("failed to decode column %q as %s: %w", column, columnTypes[i].DatabaseTypeName(), err)
			}
			row[column] = value
		}

		result = append(result, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return result, nil
}

// normalizeSQLValue converts structured PostgreSQL values returned through
// database/sql's text-oriented interface into JSON-friendly Go values. pgx
// returns json/jsonb as []byte and arrays as PostgreSQL array literals unless
// the caller provides a typed scan destination.
func normalizeSQLValue(databaseType string, value any) (any, error) {
	if value == nil {
		return nil, nil
	}

	typeName := strings.ToLower(databaseType)
	if typeName != "json" && typeName != "jsonb" && !strings.HasPrefix(typeName, "_") {
		return value, nil
	}

	var source []byte
	switch typed := value.(type) {
	case []byte:
		source = typed
	case string:
		source = []byte(typed)
	default:
		// Some drivers already decode structured values. Preserve those values.
		return value, nil
	}

	dataType, ok := postgresTypes.TypeForName(typeName)
	if !ok {
		return value, nil
	}

	if strings.HasPrefix(typeName, "_") {
		var decoded pgtype.Array[any]
		if err := postgresTypes.Scan(dataType.OID, pgtype.TextFormatCode, source, &decoded); err != nil {
			return nil, err
		}
		return nestedPostgresArray(decoded.Elements, decoded.Dims), nil
	}

	var decoded any
	if err := postgresTypes.Scan(dataType.OID, pgtype.TextFormatCode, source, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func nestedPostgresArray(elements []any, dimensions []pgtype.ArrayDimension) []any {
	if len(elements) == 0 {
		return []any{}
	}
	if len(dimensions) <= 1 {
		return elements
	}

	stride := 1
	for _, dimension := range dimensions[1:] {
		stride *= int(dimension.Length)
	}

	result := make([]any, int(dimensions[0].Length))
	for i := range result {
		start := i * stride
		result[i] = nestedPostgresArray(elements[start:start+stride], dimensions[1:])
	}
	return result
}
