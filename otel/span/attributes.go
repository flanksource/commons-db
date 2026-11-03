package span

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func text(v any) string {
	if v == nil {
		return ""
	}
	if values, ok := v.([]any); ok && len(values) == 1 {
		v = values[0]
	}
	if number, ok := v.(float64); ok {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func first(d map[string]any, paths ...string) (any, string) {
	for _, path := range paths {
		if path == "" {
			continue
		}
		value, ok := d[path]
		if !ok {
			var current any = d
			for _, part := range strings.Split(path, ".") {
				m, ok := current.(map[string]any)
				if !ok {
					current = nil
					break
				}
				current = m[part]
			}
			value = current
		}
		if value != nil && value != "" {
			return value, path
		}
	}
	return nil, ""
}

func pick(d map[string]any, paths ...string) any { v, _ := first(d, paths...); return v }

func collection(v any) (any, error) {
	if raw, ok := v.(string); ok {
		var decoded any
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			return nil, err
		}
		return decoded, nil
	}
	return v, nil
}

func mergeAttributes(into map[string]any, v any) error {
	if v == nil {
		return nil
	}
	decoded, err := collection(v)
	if err != nil {
		return err
	}
	switch value := decoded.(type) {
	case map[string]any:
		for key, child := range value {
			into[strings.ReplaceAll(key, "@", ".")] = child
		}
	case []any:
		for _, raw := range value {
			tag, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("tag must be an object")
			}
			key, ok := tag["key"].(string)
			if !ok || key == "" {
				return fmt.Errorf("tag requires key")
			}
			val, err := tagValue(tag)
			if err != nil {
				return fmt.Errorf("tag %q: %w", key, err)
			}
			into[strings.ReplaceAll(key, "@", ".")] = val
		}
	default:
		return fmt.Errorf("attributes must be an object or tag array")
	}
	return nil
}

func tagValue(tag map[string]any) (any, error) {
	for _, field := range []string{"value", "vStr", "vBool", "vLong", "vDouble"} {
		value, exists := tag[field]
		if !exists {
			continue
		}
		typed, ok := value.(map[string]any)
		if !ok {
			return value, nil
		}
		for _, key := range []string{"stringValue", "boolValue", "intValue", "doubleValue", "arrayValue", "kvlistValue", "bytesValue"} {
			if v, exists := typed[key]; exists {
				return v, nil
			}
		}
		return nil, fmt.Errorf("unsupported OTLP attribute value")
	}
	return nil, fmt.Errorf("attribute value is required")
}
