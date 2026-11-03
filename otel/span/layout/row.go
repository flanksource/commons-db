package layout

import (
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/otel/span"
)

func (d Definition) Normalize(row map[string]any) (span.Span, error) {
	targets := map[string]string{"trace_id": "traceId", "span_id": "spanId", "parent_id": "parentId", "timestamp": "startedAt", "duration_ms": "durationMs", "service": "service", "operation": "operation", "status": "status", "kind": "kind"}
	values := map[string]any{}
	if _, mapped := d.Fields["status"]; mapped {
		values["status"] = "unset"
	}
	units := map[string]string{}
	attributes := map[string]any{}
	resource := map[string]any{}
	for name, field := range d.Fields {
		value := read(row, field.Source)
		if value == nil {
			if name == "trace_id" || name == "span_id" || name == "timestamp" || name == "duration_ms" {
				return span.Span{}, fmt.Errorf("canonical %s requires source %s", name, field.Source)
			}
			continue
		}
		if key, ok := strings.CutPrefix(name, "attributes."); ok {
			var err error
			value, err = field.value(value, name, ToCanonical)
			if err != nil {
				return span.Span{}, err
			}
			attributes[key] = value
			continue
		}
		if key, ok := strings.CutPrefix(name, "resource."); ok {
			var err error
			value, err = field.value(value, name, ToCanonical)
			if err != nil {
				return span.Span{}, err
			}
			resource[key] = value
			continue
		}
		target := targets[name]
		if target == "" {
			return span.Span{}, fmt.Errorf("unknown canonical layout field %s", name)
		}
		if len(field.Values) > 0 || name == "timestamp" && (field.Type == "date" || field.Type == "date_nanos") {
			var err error
			value, err = field.value(value, name, ToCanonical)
			if err != nil {
				return span.Span{}, err
			}
		}
		values[target] = value
		units[target] = field.Unit
	}
	document := map[string]any{}
	for name, value := range row {
		document[name] = value
	}
	canonical, err := span.NormalizeMapped(document, values, units)
	if err != nil {
		return span.Span{}, err
	}
	for key, value := range attributes {
		canonical.Attributes[key] = value
	}
	for key, value := range resource {
		canonical.Resource[key] = value
	}
	return canonical, nil
}

func read(row map[string]any, path string) any {
	if value, ok := row[path]; ok {
		return value
	}
	prefix, suffix, ok := strings.Cut(path, ".")
	if !ok {
		return nil
	}
	child, ok := row[prefix].(map[string]any)
	if !ok {
		return nil
	}
	return read(child, suffix)
}
