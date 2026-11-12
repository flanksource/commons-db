package query

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/flanksource/clicky/api"
)

// TableColumnsFor projects the canonical struct schema into display order.
// Other columns remain available in the column picker with their native bindings.
func TableColumnsFor[T any](visible ...string) []api.ColumnDef {
	columns, err := ColumnsFor(reflect.TypeFor[T]())
	if err != nil {
		panic(err)
	}
	projected, err := ClickyColumns(Profile{Columns: columns})
	if err != nil {
		panic(err)
	}
	for i, column := range columns {
		if column.Hidden || column.Computed || column.Type == ColumnTypeJSON {
			continue
		}
		projected[i].SortKey = column.Name
		if column.Filter == nil || !column.Filter.Disabled {
			projected[i].FilterKey = "filter." + column.Name
		}
	}
	ordered := make([]api.ColumnDef, 0, len(columns))
	for _, name := range visible {
		index := slices.IndexFunc(projected, func(c api.ColumnDef) bool { return c.Name == name })
		if index < 0 {
			panic(fmt.Sprintf("table column %q is absent from %s", name, reflect.TypeFor[T]()))
		}
		if slices.ContainsFunc(ordered, func(c api.ColumnDef) bool { return c.Name == name }) {
			panic("duplicate table column: " + name)
		}
		column := projected[index]
		column.Hidden, column.DefaultHidden = false, false
		ordered = append(ordered, column)
	}
	for _, column := range projected {
		if !slices.Contains(visible, column.Name) {
			column.DefaultHidden = true
			ordered = append(ordered, column)
		}
	}
	return ordered
}

// TableRowFor retains JSON scalars, including exact integers, false and null.
// Invalid row types are programmer errors, just as invalid Columns tags are.
func TableRowFor(value any) map[string]any {
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Errorf("table row: %w", err))
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var row map[string]any
	if err := decoder.Decode(&row); err != nil {
		panic(fmt.Errorf("table row: %w", err))
	}
	if row == nil {
		panic("table row must be a JSON object")
	}
	projectNativeFields(row, reflect.ValueOf(value))
	return row
}

func projectNativeFields(row map[string]any, value reflect.Value) {
	for value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return
	}
	for _, field := range reflect.VisibleFields(value.Type()) {
		if !field.IsExported() || field.Anonymous {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" {
			name = field.Name
		}
		if _, exists := row[name]; !exists {
			continue
		}
		v, err := value.FieldByIndexErr(field.Index)
		if err != nil {
			continue // An absent embedded pointer has no serialized fields.
		}
		for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
			if v.IsNil() {
				break
			}
			v = v.Elem()
		}
		switch v.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			row[name] = v.Int()
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			row[name] = v.Uint()
		case reflect.Float32, reflect.Float64:
			row[name] = v.Float()
		case reflect.Bool:
			row[name] = v.Bool()
		case reflect.String:
			row[name] = v.String()
		}
	}
}
