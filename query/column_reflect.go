package query

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/clicky/api"
)

var (
	reflectTimeType        = reflect.TypeOf(time.Time{})
	reflectDurationType    = reflect.TypeOf(time.Duration(0))
	reflectRawMessageType  = reflect.TypeOf(json.RawMessage(nil))
	reflectTextMarshalType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

// ColumnsFor reflects a row struct into the columns a Profile over its JSON
// encoding declares, so a typed API derives its schema from the type it
// serves rather than restating it by hand.
//
// It reads the tags clicky already reads, so an entity and a profile over the
// same struct agree on what each field is called and how it renders:
//
//   - json names the column; `json:"-"` and unexported fields are skipped, and
//     an embedded struct is flattened exactly as encoding/json flattens it.
//   - pretty is parsed by clicky's own grammar (api.ParsePrettyTagWithName):
//     label= sets Label, format= sets Format, and hide or "-" sets Hidden.
//     Three keys clicky carries through untouched add profile semantics it has
//     no word for: type= (a ColumnType), kind= (a ColumnKind) and unit=.
//   - sort is clicky's public sort key. A profile orders by column name, so
//     the key must be that name.
//   - filter overrides the inferred filter: a kind (terms, exact, text, range,
//     duration, date, time, boolean, none), field=, limit=, options=a|b,
//     lookup=bool, multi=bool, disabled, or "-" for no filter at all.
//   - a string list ([]string) is a json column filtered as an array: a value
//     selection over its elements (ColumnFilterDef.Array), with no tag needed.
//
// Type is inferred from the Go type and overridden by pretty type=. A key or a
// value no consumer would understand is an error, never ignored.
func ColumnsFor(rowType reflect.Type) ([]ColumnDef, error) {
	rowType = dereferenceReflectType(rowType)
	if rowType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("row type %s must be a struct", rowType)
	}
	columns, err := reflectColumns(rowType)
	if err != nil {
		return nil, fmt.Errorf("row type %s: %w", rowType, err)
	}
	seen := make(map[string]bool, len(columns))
	for _, column := range columns {
		if seen[column.Name] {
			return nil, fmt.Errorf("row type %s: column %q is declared twice", rowType, column.Name)
		}
		seen[column.Name] = true
	}
	return columns, nil
}

func reflectColumns(rowType reflect.Type) ([]ColumnDef, error) {
	var columns []ColumnDef
	for index := range rowType.NumField() {
		field := rowType.Field(index)
		name, asString, skip := reflectJSONName(field)
		if skip {
			continue
		}
		if field.Anonymous && name == "" && dereferenceReflectType(field.Type).Kind() == reflect.Struct {
			nested, err := reflectColumns(dereferenceReflectType(field.Type))
			if err != nil {
				return nil, err
			}
			columns = append(columns, nested...)
			continue
		}
		if !field.IsExported() {
			continue
		}
		if name == "" {
			name = field.Name
		}
		column, err := reflectColumn(field, name, asString)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.Name, err)
		}
		columns = append(columns, column)
	}
	return columns, nil
}

// reflectJSONName reads the json tag the way encoding/json does: the name
// before the first comma, "-" to skip, and the string option that encodes a
// scalar as a JSON string.
func reflectJSONName(field reflect.StructField) (name string, asString bool, skip bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	name, options, _ := strings.Cut(tag, ",")
	return name, slices.Contains(strings.Split(options, ","), "string"), false
}

func reflectColumn(field reflect.StructField, name string, asString bool) (ColumnDef, error) {
	column := ColumnDef{Name: name, Type: ColumnTypeString}
	if !asString {
		inferred, err := reflectColumnType(field.Type)
		if err != nil {
			return ColumnDef{}, err
		}
		column.Type = inferred
	}
	if err := applyPrettyTag(&column, field.Tag.Get("pretty")); err != nil {
		return ColumnDef{}, err
	}
	if column.Type == ColumnTypeNumber && column.Format == "" && isIntegerKind(field.Type) {
		column.Format = api.FormatInteger
	}
	if err := applySortTag(column, field.Tag); err != nil {
		return ColumnDef{}, err
	}
	filter, err := parseFilterTag(field.Tag)
	if err != nil {
		return ColumnDef{}, err
	}
	if !asString && isStringList(field.Type) {
		if filter, err = arrayColumnFilter(filter); err != nil {
			return ColumnDef{}, err
		}
	}
	column.Filter = filter
	if err := column.Validate(); err != nil {
		return ColumnDef{}, err
	}
	return column, nil
}

// reflectColumnType is the ColumnType a Go type's JSON encoding has.
func reflectColumnType(goType reflect.Type) (ColumnType, error) {
	goType = dereferenceReflectType(goType)
	switch {
	case goType == reflectDurationType:
		return "", fmt.Errorf(
			"a time.Duration encodes as nanoseconds, which no column unit reads; expose milliseconds as a number tagged pretty:\"type=duration,unit=ms\"")
	case goType == reflectTimeType:
		return ColumnTypeDateTime, nil
	case goType == reflectRawMessageType:
		return ColumnTypeJSON, nil
	case goType.Implements(reflectTextMarshalType) || reflect.PointerTo(goType).Implements(reflectTextMarshalType):
		return ColumnTypeString, nil
	}
	switch goType.Kind() {
	case reflect.String:
		return ColumnTypeString, nil
	case reflect.Bool:
		return ColumnTypeBoolean, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return ColumnTypeNumber, nil
	case reflect.Slice:
		if goType.Elem().Kind() == reflect.Uint8 {
			return ColumnTypeString, nil
		}
		return ColumnTypeJSON, nil
	case reflect.Array, reflect.Map, reflect.Struct, reflect.Interface:
		return ColumnTypeJSON, nil
	default:
		return "", fmt.Errorf("a %s cannot be encoded as JSON, so it has no column", goType.Kind())
	}
}

// isIntegerKind reports a field holding a whole number. Its column is typed
// number like a float's, and an index or JSON reads it back as a float64, so
// only the column's format keeps it rendering without decimals.
func isIntegerKind(goType reflect.Type) bool {
	switch dereferenceReflectType(goType).Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	}
	return false
}

// isStringList reports a field whose JSON encoding is an array of strings: a
// column of several values per row.
func isStringList(goType reflect.Type) bool {
	goType = dereferenceReflectType(goType)
	return (goType.Kind() == reflect.Slice || goType.Kind() == reflect.Array) && goType.Elem().Kind() == reflect.String
}

// arrayColumnFilter is the filter a string list column gets: a value selection
// over its elements, whatever else the tag declared about it. Stored as JSON,
// the list's own text is not a value anyone could select, so a kind that would
// compare it whole is refused.
func arrayColumnFilter(declared *ColumnFilterDef) (*ColumnFilterDef, error) {
	if declared == nil {
		return &ColumnFilterDef{Kind: ColumnFilterKindTerms, Array: true}, nil
	}
	if declared.Disabled {
		return declared, nil
	}
	if kind := declared.Kind.Normalized(); kind != ColumnFilterKindTerms {
		return nil, fmt.Errorf("a string list filters by its elements' values; kind %q cannot compare them", kind)
	}
	declared.Kind, declared.Array = ColumnFilterKindTerms, true
	return declared, nil
}

func dereferenceReflectType(goType reflect.Type) reflect.Type {
	for goType.Kind() == reflect.Pointer {
		goType = goType.Elem()
	}
	return goType
}
