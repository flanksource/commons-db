package query

import (
	"reflect"
	"strings"
)

func isSampleKeyValueList(value reflect.Value) bool {
	if value.Len() == 0 {
		return false
	}
	for i := 0; i < value.Len(); i++ {
		item := unwrapSampleValue(value.Index(i))
		if !item.IsValid() || item.Kind() != reflect.Map || item.Type().Key().Kind() != reflect.String {
			return false
		}
		hasKey, hasValue := false, false
		iterator := item.MapRange()
		for iterator.Next() {
			name := strings.ToLower(iterator.Key().String())
			switch name {
			case "key", "name":
				entry := unwrapSampleValue(iterator.Value())
				hasKey = entry.IsValid() && entry.Kind() == reflect.String
			case "value":
				hasValue = true
			}
		}
		if !hasKey || !hasValue {
			return false
		}
	}
	return true
}

func isSampleScalar(value reflect.Value) bool {
	value = unwrapSampleValue(value)
	if !value.IsValid() {
		return true
	}
	switch value.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

func unwrapSampleValue(value reflect.Value) reflect.Value {
	for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
		if value.IsNil() {
			return reflect.Value{}
		}
		value = value.Elem()
	}
	return value
}
