package providers

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/commons-db/query"
)

func openTelemetryRow(document map[string]any, options openTelemetryOptions) query.Row {
	attributes := flattenTraceDocument(document)
	duration, durationField := firstTracePathWithName(document, "duration_ms", "duration", "durationNano", "duration_nano")
	row := query.Row{
		"timestamp":   normalizeTraceTimestamp(stringTraceValue(firstTracePath(document, options.DateField, "@timestamp", "timestamp", "startTimeMillis"))),
		"trace_id":    stringTraceValue(firstTracePath(document, options.TraceIDField, "trace_id", "traceID")),
		"span_id":     stringTraceValue(firstTracePath(document, options.SpanIDField, "span_id", "spanID")),
		"parent_id":   traceParentID(document, options),
		"service":     stringTraceValue(firstTracePath(document, options.ServiceField, "service_name", "process.serviceName")),
		"operation":   stringTraceValue(firstTracePath(document, options.OperationField, "operation_name", "operationName")),
		"status":      traceStatus(document, attributes, options),
		"duration_ms": traceDurationMillis(duration, durationField, options.Format),
		"_attributes": attributes,
	}
	row["id"] = row["span_id"]
	row["service_name"] = row["service"]
	row["operation_name"] = row["operation"]
	for name, value := range attributes {
		normalized := normalizeTraceAttributeName(name)
		if _, exists := row[normalized]; !exists {
			row[normalized] = value
		}
	}
	return row
}

func traceParentID(document map[string]any, options openTelemetryOptions) string {
	if options.Format == "jaeger" {
		if references, ok := document["references"].([]any); ok {
			for _, raw := range references {
				reference, _ := raw.(map[string]any)
				if options.ParentRefType != "" && stringTraceValue(reference["refType"]) != options.ParentRefType {
					continue
				}
				if id := stringTraceValue(firstTracePath(reference, "spanID", "span_id")); id != "" {
					return id
				}
			}
		}
	}
	return stringTraceValue(firstTracePath(document, options.ParentIDField, "parent_id", "parentID"))
}

func traceStatus(document, attributes map[string]any, options openTelemetryOptions) string {
	fields := append(append([]string{}, options.StatusFields...), "status", "status.code", "tag.error", "error")
	for _, field := range fields {
		value := stringTraceValue(firstTracePath(document, field))
		if value == "" {
			value = stringTraceValue(attributes[field])
		}
		if value != "" {
			return strings.ToLower(value)
		}
	}
	return ""
}

func flattenTraceDocument(document map[string]any) map[string]any {
	result := map[string]any{}
	var walk func(string, any)
	walk = func(prefix string, value any) {
		switch typed := value.(type) {
		case map[string]any:
			for name, child := range typed {
				next := name
				if prefix != "" {
					next = prefix + "." + name
				}
				walk(next, child)
			}
		case []any:
			if isJaegerTagList(prefix, typed) {
				for _, raw := range typed {
					tag, _ := raw.(map[string]any)
					name := stringTraceValue(tag["key"])
					if name != "" {
						result["tag."+name] = firstTracePath(tag, "value", "vStr", "vDouble", "vBool", "vLong")
					}
				}
				return
			}
			result[prefix] = typed
		default:
			result[prefix] = typed
		}
	}
	walk("", document)
	return result
}

func normalizeTraceAttributeName(name string) string {
	name = strings.TrimPrefix(name, "tag.")
	return strings.ReplaceAll(name, "@", ".")
}

func isJaegerTagList(prefix string, values []any) bool {
	if prefix != "tags" && prefix != "process.tags" || len(values) == 0 {
		return false
	}
	first, ok := values[0].(map[string]any)
	if !ok {
		return false
	}
	_, ok = first["key"]
	return ok
}

func firstTracePath(document map[string]any, paths ...string) any {
	value, _ := firstTracePathWithName(document, paths...)
	return value
}

func firstTracePathWithName(document map[string]any, paths ...string) (any, string) {
	for _, path := range paths {
		if value := lookupTracePath(document, path); value != nil {
			return value, path
		}
	}
	return nil, ""
}

func lookupTracePath(document map[string]any, path string) any {
	if document == nil || path == "" {
		return nil
	}
	if value, ok := document[path]; ok {
		return value
	}
	var current any = document
	for _, part := range strings.Split(path, ".") {
		mapping, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = mapping[part]
		if current == nil {
			return nil
		}
	}
	return current
}

func unwrapTraceValue(value any) any {
	if values, ok := value.([]any); ok && len(values) == 1 {
		return values[0]
	}
	return value
}

func stringTraceValue(value any) string {
	switch typed := unwrapTraceValue(value).(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		if math.Trunc(typed) == typed {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func traceDurationMillis(value any, field, format string) float64 {
	var number float64
	switch typed := unwrapTraceValue(value).(type) {
	case float64:
		number = typed
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case string:
		number, _ = strconv.ParseFloat(typed, 64)
	}
	if format == "jaeger" {
		return math.Round(number/10) / 100
	}
	if field == "duration_ms" {
		return number
	}
	return number / 1_000_000
}

func normalizeTraceTimestamp(raw string) string {
	if raw == "" {
		return ""
	}
	if number, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if number > 10_000_000_000_000 {
			return time.Unix(0, number).Format(time.RFC3339Nano)
		}
		if number > 10_000_000_000 {
			return time.UnixMilli(number).Format(time.RFC3339Nano)
		}
		return time.Unix(number, 0).Format(time.RFC3339Nano)
	}
	if timestamp, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return timestamp.Format(time.RFC3339Nano)
	}
	return raw
}
