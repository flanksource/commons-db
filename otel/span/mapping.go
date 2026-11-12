package span

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

// IsCanonicalField reports whether a mapping target belongs to the canonical model.
func IsCanonicalField(name string) bool { _, ok := spanFields[name]; return ok }
func ValidMappingUnit(unit string) bool { _, ok := mappingUnit(unit); return ok }

var spanFields = map[string]string{"traceId": "trace_id", "spanId": "span_id", "parentId": "parent_id", "startedAt": "timestamp", "durationMs": "duration_ms", "service": "service", "operation": "operation", "status": "status", "kind": "kind", "attributes": "_span_attributes", "resource": "_resource"}

func mappingUnit(unit string) (time.Duration, bool) {
	value, ok := map[string]time.Duration{"s": time.Second, "ms": time.Millisecond, "us": time.Microsecond, "ns": time.Nanosecond}[unit]
	return value, ok
}

func NormalizeMapped(document map[string]any, values map[string]any, units map[string]string) (Span, error) {
	options := Options{}
	if document["spanID"] != nil && (document["startTime"] != nil || document["startTimeMillis"] != nil) {
		options.Format = "jaeger"
	}
	for name, value := range values {
		if !IsCanonicalField(name) {
			return Span{}, fmt.Errorf("unknown canonical span field %q", name)
		}
		if units[name] != "" {
			unit, valid := mappingUnit(units[name])
			if !valid || (name != "startedAt" && name != "durationMs") {
				return Span{}, fmt.Errorf("invalid unit %q for span %s", units[name], name)
			}
			if name == "startedAt" {
				integer, err := strconv.ParseInt(text(value), 10, 64)
				if err != nil {
					return Span{}, fmt.Errorf("span startedAt: %w", err)
				}
				seconds := integer / int64(time.Second/unit)
				remainder := integer % int64(time.Second/unit)
				value = time.Unix(seconds, remainder*int64(unit)).UTC()
			} else {
				number, err := strconv.ParseFloat(fmt.Sprint(value), 64)
				if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
					return Span{}, fmt.Errorf("span durationMs: invalid number %v", value)
				}
				value = number * float64(unit) / float64(time.Millisecond)
			}
		}
		candidates := map[string][]string{
			"traceId": {"trace_id", "traceId", "traceID", "trace.id"}, "spanId": {"span_id", "spanId", "spanID", "span.id"},
			"parentId":   {"parent_id", "parentId", "parentID", "parentSpanId", "references"},
			"attributes": {"attributes", "tags", "_span_attributes", "_attributes"}, "resource": {"resource", "resource.attributes", "process.tags", "_resource"},
			"service": {"service", "service_name", "serviceName", "process.serviceName"}, "operation": {"operation", "operation_name", "operationName", "name"},
			"status": {"status", "status.code", "tag.error", "error", "otel.status_code"}, "kind": {"kind", "span.kind", "tag.span@kind"},
		}
		for _, key := range candidates[name] {
			delete(document, key)
		}
		target := spanFields[name]
		document[target] = value
		switch name {
		case "traceId":
			options.TraceIDField = target
		case "spanId":
			options.SpanIDField = target
		case "parentId":
			options.ParentIDField = target
		case "startedAt":
			options.DateField = target
		case "service":
			options.ServiceField = target
		case "operation":
			options.OperationField = target
		case "status":
			options.StatusFields = []string{target}
		}
	}
	return Normalize(document, options)
}
