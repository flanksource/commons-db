package span

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type Span struct {
	TraceID    string          `json:"traceId"`
	SpanID     string          `json:"spanId"`
	ParentID   string          `json:"parentId,omitempty"`
	StartedAt  time.Time       `json:"startedAt"`
	DurationMs float64         `json:"durationMs"`
	Service    string          `json:"service"`
	Operation  string          `json:"operation"`
	Status     string          `json:"status"`
	Kind       string          `json:"kind,omitempty"`
	Attributes map[string]any  `json:"attributes,omitempty"`
	Resource   map[string]any  `json:"resource,omitempty"`
	OTLP       json.RawMessage `json:"otlp,omitempty"`
}

type Options struct {
	Format                                              string
	DateField, TraceIDField, SpanIDField, ParentIDField string
	ServiceField, OperationField, ParentRefType         string
	StatusFields                                        []string
}

func Normalize(d map[string]any, o Options) (Span, error) {
	s := Span{Attributes: map[string]any{}, Resource: map[string]any{}}
	s.TraceID = text(pick(d, o.TraceIDField, "trace_id", "traceId", "traceID", "trace.id"))
	s.SpanID = text(pick(d, o.SpanIDField, "span_id", "spanId", "spanID", "span.id"))
	if s.TraceID == "" || s.SpanID == "" {
		return s, fmt.Errorf("span requires trace_id and span_id")
	}
	format := o.Format
	if format == "" && pick(d, "spanID") != nil && pick(d, "startTime", "startTimeMillis") != nil {
		format = "jaeger"
	}
	if format == "" && pick(d, "span_id") != nil && pick(d, "@timestamp") != nil {
		format = "flat"
	}
	raw, field := first(d, o.DateField, "startTimeUnixNano", "startTime", "startTimeMillis", "@timestamp", "timestamp", "startedAt")
	var err error
	s.StartedAt, err = timestamp(raw, field, format)
	if err != nil {
		return s, err
	}
	s.DurationMs, err = duration(d, format, s.StartedAt)
	if err != nil {
		return s, err
	}
	s.ParentID = text(pick(d, o.ParentIDField, "parent_id", "parentId", "parentID", "parentSpanId"))
	if s.ParentID == "" && pick(d, "references") != nil {
		refs, err := collection(pick(d, "references"))
		if err != nil {
			return s, fmt.Errorf("references: %w", err)
		}
		values, ok := refs.([]any)
		if !ok {
			return s, fmt.Errorf("references must be an array")
		}
		for _, value := range values {
			ref, ok := value.(map[string]any)
			if !ok {
				return s, fmt.Errorf("reference must be an object")
			}
			kind := text(ref["refType"])
			expected := o.ParentRefType
			if expected == "" {
				expected = "CHILD_OF"
			}
			if kind != expected {
				continue
			}
			parent := text(pick(ref, "spanID", "spanId", "span_id"))
			if parent == "" {
				return s, fmt.Errorf("parent reference requires spanID")
			}
			if s.ParentID != "" && s.ParentID != parent {
				return s, fmt.Errorf("multiple parent references")
			}
			s.ParentID = parent
		}
	}
	for _, key := range []string{"attributes", "tags"} {
		if err := mergeAttributes(s.Attributes, pick(d, key)); err != nil {
			return s, fmt.Errorf("%s: %w", key, err)
		}
	}
	for key, value := range d {
		if strings.HasPrefix(key, "tag.") {
			s.Attributes[strings.ReplaceAll(strings.TrimPrefix(key, "tag."), "@", ".")] = value
		}
	}
	flattened := map[string]any{}
	if err := mergeAttributes(flattened, d["_attributes"]); err != nil {
		return s, fmt.Errorf("_attributes: %w", err)
	}
	for key, value := range flattened {
		if name, ok := strings.CutPrefix(key, "resource."); ok {
			s.Resource[strings.TrimPrefix(name, "attributes.")] = value
		} else {
			s.Attributes[strings.TrimPrefix(strings.TrimPrefix(key, "tag."), "attributes.")] = value
		}
	}
	if err := mergeAttributes(s.Attributes, pick(d, "_span_attributes")); err != nil {
		return s, fmt.Errorf("_span_attributes: %w", err)
	}
	for _, key := range []string{"resource.attributes", "process.tags", "_resource"} {
		if err := mergeAttributes(s.Resource, pick(d, key)); err != nil {
			return s, fmt.Errorf("%s: %w", key, err)
		}
	}
	if pick(d, "resource.attributes") == nil {
		if err := mergeAttributes(s.Resource, pick(d, "resource")); err != nil {
			return s, fmt.Errorf("resource: %w", err)
		}
	}
	s.Service = text(pick(d, o.ServiceField, "service", "service_name", "serviceName", "process.serviceName", "resource.attributes.service.name"))
	if s.Service == "" {
		s.Service = text(s.Resource["service.name"])
	}
	if s.Service != "" {
		s.Resource["service.name"] = s.Service
	}
	s.Operation = text(pick(d, o.OperationField, "operation", "operation_name", "operationName", "name"))
	s.Kind = text(pick(d, "kind", "span.kind", "tag.span@kind"))
	status := ""
	for _, field := range append(append([]string{}, o.StatusFields...), "status.code", "status", "tag.error", "error", "otel.status_code") {
		value := pick(d, field)
		if value == nil {
			value = pick(s.Attributes, strings.ReplaceAll(strings.TrimPrefix(field, "tag."), "@", "."))
		}
		if value != nil {
			status = text(value)
			break
		}
	}
	switch strings.TrimPrefix(strings.ToLower(status), "status_code_") {
	case "", "0", "false", "unset":
		s.Status = "unset"
	case "1", "ok":
		s.Status = "ok"
	case "2", "true", "error":
		s.Status = "error"
	default:
		return s, fmt.Errorf("unknown span status %q", status)
	}
	return s, nil
}

func (s Span) Row() map[string]any {
	return map[string]any{"trace_id": s.TraceID, "span_id": s.SpanID, "parent_id": s.ParentID, "timestamp": s.StartedAt.UTC().Format(time.RFC3339Nano), "duration_ms": s.DurationMs, "service": s.Service, "operation": s.Operation, "status": s.Status, "kind": s.Kind, "_span_attributes": s.Attributes, "_resource": s.Resource}
}

func duration(d map[string]any, format string, start time.Time) (float64, error) {
	v, key := first(d, "duration_ms", "durationMs", "durationNano", "duration_nano", "durationNanos", "duration")
	if v == nil {
		end, endKey := first(d, "endTimeUnixNano", "endTimeMillis")
		if end == nil {
			return 0, fmt.Errorf("span requires duration or end time")
		}
		at, err := timestamp(end, endKey, format)
		if err != nil {
			return 0, err
		}
		v = at.Sub(start).Seconds() * 1000
		key = "duration_ms"
	}
	if typed, ok := v.(time.Duration); ok {
		if typed < 0 {
			return 0, fmt.Errorf("negative duration")
		}
		return float64(typed) / float64(time.Millisecond), nil
	}
	n, err := strconv.ParseFloat(text(v), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return 0, fmt.Errorf("invalid %s %v", key, v)
	}
	switch key {
	case "duration_ms", "durationMs":
		return n, nil
	case "duration":
		switch format {
		case "jaeger":
			return n / 1000, nil
		case "flat":
			return n / 1e6, nil
		default:
			return 0, fmt.Errorf("duration requires a recognized format or explicit unit")
		}
	default:
		return n / 1e6, nil
	}
}

func timestamp(v any, field, format string) (time.Time, error) {
	if at, ok := v.(time.Time); ok {
		return at, nil
	}
	raw := text(v)
	if at, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return at, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s timestamp %q", field, raw)
	}
	switch {
	case strings.HasSuffix(field, "UnixNano") || n > 1e16:
		return time.Unix(0, n).UTC(), nil
	case field == "startTime" && format == "jaeger" || n > 1e13:
		return time.UnixMicro(n).UTC(), nil
	case strings.HasSuffix(field, "Millis") || n > 1e10:
		return time.UnixMilli(n).UTC(), nil
	default:
		return time.Unix(n, 0).UTC(), nil
	}
}
