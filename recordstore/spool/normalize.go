// Row values as a sqlite kind table stores them, so a spooled row and the
// same row appended directly store identical values.
package spool

import (
	"encoding/json"
	"fmt"

	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// Normalize converts row into the values sqlitetable.Value stores for
// schema's columns: a structured column's value as its JSON text, a time as
// sqlitetable.TimeLayout, and any other value the driver cannot store as its
// JSON text. A key schema does not declare is an error, but for a dynamic
// kind, whose key gets the value its inferred column stores — a null none, an
// object or an array itself, for the owner to infer its column from.
func Normalize(schema recordstore.KindSchema, row recordstore.Row) (recordstore.Row, error) {
	types := columnTypes(schema)
	normalized := make(recordstore.Row, len(row))
	for key, value := range row {
		columnType, ok := types[key]
		if !ok && schema.Options.Dynamic {
			inferred, typed := recordstore.InferColumnType(value)
			switch {
			case !typed:
				continue
			case inferred == query.ColumnTypeJSON:
				normalized[key] = value
				continue
			}
			columnType, ok = inferred, true
		}
		if !ok {
			return nil, fmt.Errorf("key %q is not a column of kind %q", key, schema.Kind)
		}
		stored, err := sqlitetable.Value(columnType, value)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", key, err)
		}
		normalized[key] = stored
	}
	return normalized, nil
}

// denormalize turns a loaded row's structured columns back into JSON, so
// storing it encodes the text Normalize wrote rather than a string of it.
func denormalize(types map[string]query.ColumnType, row recordstore.Row) error {
	for key, value := range row {
		text, ok := value.(string)
		if !ok || !sqlitetable.IsStructured(types[key]) {
			continue
		}
		if !json.Valid([]byte(text)) {
			return fmt.Errorf("column %q holds %q, which is not the JSON text of a %s value", key, text, types[key])
		}
		row[key] = json.RawMessage(text)
	}
	return nil
}

func columnTypes(schema recordstore.KindSchema) map[string]query.ColumnType {
	types := make(map[string]query.ColumnType, len(schema.Columns))
	for _, column := range schema.Columns {
		types[column.Name] = column.Type
	}
	return types
}
