package tracequery

import (
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/commons-db/query"
)

// FromRow projects a provider row into the trace view without losing typed attributes.
func FromRow(row query.Row) (Span, error) {
	text := func(key string) string { value, _ := row[key].(string); return value }
	span := Span{TraceID: text("trace_id"), SpanID: text("span_id"), ParentID: text("parent_id"), Service: text("service"), Operation: text("operation"), Kind: text("kind"), Attributes: map[string]any{}, Resource: map[string]any{}}
	var err error
	span.StartedAt, err = time.Parse(time.RFC3339Nano, text("timestamp"))
	if err != nil {
		return span, fmt.Errorf("span %s/%s timestamp: %w", span.TraceID, span.SpanID, err)
	}
	span.DurationMs, _ = row["duration_ms"].(float64)
	status := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(text("status"))), "status_code_")
	switch status {
	case "", "unset", "0", "false":
		span.Status = "unset"
	case "ok", "1":
		span.Status = "ok"
	case "error", "2", "true":
		span.Status = "error"
	default:
		return span, fmt.Errorf("span %s has unknown status %q", span.SpanID, status)
	}
	attributes, _ := row["_attributes"].(map[string]any)
	for key, value := range attributes {
		if name, ok := strings.CutPrefix(key, "resource."); ok {
			span.Resource[strings.TrimPrefix(name, "attributes.")] = value
		} else {
			span.Attributes[strings.TrimPrefix(strings.TrimPrefix(key, "tag."), "attributes.")] = value
		}
	}
	if span.Service != "" {
		span.Resource["service.name"] = span.Service
	}
	for key, value := range rowMap(row, "_span_attributes") {
		span.Attributes[key] = value
	}
	for key, value := range rowMap(row, "_resource") {
		span.Resource[key] = value
	}
	if span.Service == "" {
		if value, ok := span.Resource["service.name"]; ok {
			service, ok := value.(string)
			if !ok {
				return span, fmt.Errorf("span %s service.name must be a string", span.SpanID)
			}
			span.Service = service
		}
	}
	if _, err := groups([]Span{span}); err != nil {
		return span, err
	}
	return span, nil
}

func rowMap(row query.Row, key string) map[string]any {
	value, _ := row[key].(map[string]any)
	return value
}
