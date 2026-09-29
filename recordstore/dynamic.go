// Dynamic kinds: a column for every key a kind's rows bring, typed by the JSON
// value class of the first value seen.
package recordstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/flanksource/commons-db/query"
)

// ErrSchemaMismatch reports an append refused because a value's type differs
// from its column's: an inferred column keeps the type its first value gave
// it, and one append may not give a new column two. None of the refused
// append's rows are written.
var ErrSchemaMismatch = errors.New("record value mismatches its column type")

// defaultDynamicColumns is how many columns a dynamic kind may infer when it
// sets no limit of its own.
const defaultDynamicColumns = 256

// DynamicColumnLimit is how many columns a dynamic kind may infer beyond the
// ones it declares.
func (o KindOptions) DynamicColumnLimit() int {
	if o.MaxDynamicColumns > 0 {
		return o.MaxDynamicColumns
	}
	return defaultDynamicColumns
}

// InferColumnType is the column type a dynamic kind gives a key first seen
// holding value, by the class of value's JSON: string, number, boolean, or
// json for an object or an array. A null infers nothing. A time is its text:
// a datetime is never inferred, because a row that reached the store through
// JSON would hold the same instant as a string and infer another type.
func InferColumnType(value any) (query.ColumnType, bool) {
	switch typed := value.(type) {
	case nil:
		return "", false
	case string, []byte, time.Time:
		return query.ColumnTypeString, true
	case bool:
		return query.ColumnTypeBoolean, true
	case json.Number:
		return query.ColumnTypeNumber, true
	case json.RawMessage:
		return inferJSON(typed)
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Pointer:
		if reflected.IsNil() {
			return "", false
		}
		return InferColumnType(reflected.Elem().Interface())
	case reflect.Bool:
		return query.ColumnTypeBoolean, true
	case reflect.String:
		return query.ColumnTypeString, true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return query.ColumnTypeNumber, true
	default:
		return query.ColumnTypeJSON, true
	}
}

// inferJSON types raw JSON by its first token.
func inferJSON(raw json.RawMessage) (query.ColumnType, bool) {
	trimmed := bytes.TrimSpace(raw)
	switch {
	case len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")):
		return "", false
	case trimmed[0] == '"':
		return query.ColumnTypeString, true
	case trimmed[0] == 't' || trimmed[0] == 'f':
		return query.ColumnTypeBoolean, true
	case trimmed[0] == '{' || trimmed[0] == '[':
		return query.ColumnTypeJSON, true
	default:
		return query.ColumnTypeNumber, true
	}
}
