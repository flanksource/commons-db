package query

import (
	"encoding/json"
	"strconv"

	"github.com/flanksource/commons-db/otel/span"
)

func copyMappingRows(rows []Row) ([]Row, error) {
	result := make([]Row, len(rows))
	for index, row := range rows {
		if err := validateAcyclicValue(row); err != nil {
			return nil, err
		}
		result[index] = copyMappingValue(row).(Row)
	}
	return result, nil
}

func copyMappingValue(value any) any {
	switch value := value.(type) {
	case json.Number:
		if integer, err := value.Int64(); err == nil {
			return copyMappingValue(integer)
		}
		if integer, err := strconv.ParseUint(value.String(), 10, 64); err == nil {
			return copyMappingValue(integer)
		}
		return value
	case int64:
		if value > 9007199254740991 || value < -9007199254740991 {
			return strconv.FormatInt(value, 10)
		}
		return value
	case uint64:
		if value > 9007199254740991 {
			return strconv.FormatUint(value, 10)
		}
		return value
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			result[key] = copyMappingValue(child)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for index, child := range value {
			result[index] = copyMappingValue(child)
		}
		return result
	case span.Span:
		value.Attributes = copyMappingValue(value.Attributes).(map[string]any)
		value.Resource = copyMappingValue(value.Resource).(map[string]any)
		return value
	default:
		return value
	}
}
