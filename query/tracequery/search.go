package tracequery

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"
)

// isError reads the canonical status every span source normalizes to.
func isError(status string) bool { return status == "error" }

func duration(value string) (float64, error) {
	if value == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid duration %q: expected a nonnegative duration such as 100ms", value)
	}
	return float64(d) / float64(time.Millisecond), nil
}

func (r Request) validate() error {
	min, err := duration(r.MinDuration)
	if err != nil {
		return err
	}
	max, err := duration(r.MaxDuration)
	if err != nil {
		return err
	}
	if r.MaxDuration != "" && max < min {
		return fmt.Errorf("maximum duration must be at least minimum duration")
	}
	if r.Limit < 0 || r.Limit > 1000 {
		return fmt.Errorf("trace limit must be between 1 and 1000")
	}
	if r.MinSpans < 0 {
		return fmt.Errorf("minimum spans must not be negative")
	}
	anyValue := func(string) error { return nil }
	if err := errors.Join(r.Services.validate("service", anyValue), r.Operations.validate("operation", anyValue), r.Statuses.validate("status", validStatus), r.Attributes.validate("attribute", validAttribute)); err != nil {
		return err
	}
	if r.Format != "" && r.Format != "flat" && r.Format != "jaeger" {
		return fmt.Errorf("format must be flat or jaeger")
	}
	for _, value := range []string{r.From, r.Until} {
		if value != "" {
			if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
				return fmt.Errorf("invalid timestamp %q: %w", value, err)
			}
		}
	}
	return nil
}

func groups(spans []Span) (map[string][]Span, error) {
	result := map[string][]Span{}
	seen := map[string]Span{}
	for _, span := range spans {
		if span.TraceID == "" || span.SpanID == "" || span.StartedAt.IsZero() || math.IsNaN(span.DurationMs) || math.IsInf(span.DurationMs, 0) || span.DurationMs < 0 || span.DurationMs >= float64(math.MaxInt64)/float64(time.Millisecond) {
			return nil, fmt.Errorf("invalid span %q in trace %q: identity, start time and a representable nonnegative duration are required", span.SpanID, span.TraceID)
		}
		key := span.TraceID + "/" + span.SpanID
		if old, ok := seen[key]; ok {
			if !reflect.DeepEqual(old, span) {
				return nil, fmt.Errorf("conflicting span %s", key)
			}
			continue
		}
		seen[key] = span
		result[span.TraceID] = append(result[span.TraceID], span)
	}
	return result, nil
}

// summarize is a trace's summary and its root span: the parentless one, or the earliest.
func summarize(spans []Span) (Summary, Span) {
	first := spans[0]
	start, end := first.StartedAt, first.StartedAt
	root := first
	services := map[string]bool{}
	errors := 0
	for _, span := range spans {
		if span.StartedAt.Before(start) {
			start = span.StartedAt
		}
		if finish := span.StartedAt.Add(time.Duration(span.DurationMs * float64(time.Millisecond))); finish.After(end) {
			end = finish
		}
		if span.ParentID == "" && root.ParentID != "" {
			root = span
		} else if (span.ParentID == "" || root.ParentID != "") && span.StartedAt.Before(root.StartedAt) {
			root = span
		}
		services[span.Service] = true
		if isError(span.Status) {
			errors++
		}
	}
	result := Summary{TraceID: first.TraceID, Service: root.Service, Operation: root.Operation, StartedAt: start, DurationMs: float64(end.Sub(start)) / float64(time.Millisecond), SpanCount: len(spans), Errors: errors, Services: []string{}}
	for service := range services {
		result.Services = append(result.Services, service)
	}
	slices.Sort(result.Services)
	return result, root
}

// inWindow reports whether span starts in the request's window and trace.
func inWindow(span Span, r Request) bool {
	if r.From != "" {
		from, _ := time.Parse(time.RFC3339Nano, r.From)
		if span.StartedAt.Before(from) {
			return false
		}
	}
	if r.Until != "" {
		until, _ := time.Parse(time.RFC3339Nano, r.Until)
		if !span.StartedAt.Before(until) {
			return false
		}
	}
	return r.TraceID == "" || r.TraceID == span.TraceID
}

// passes reports whether a trace passes every filter but the root operation
// and span-count ones, which the root operation counts leave out.
func (r Request) passes(summary Summary, spans []Span, min, max float64) bool {
	return summary.DurationMs >= min && (r.MaxDuration == "" || summary.DurationMs <= max) &&
		r.Services.pass(func(service string) bool { return slices.Contains(summary.Services, service) }) &&
		r.Statuses.pass(func(status string) bool { return (status == "error") == (summary.Errors > 0) }) &&
		r.Attributes.passAttributes(spans)
}

// Search filters whole traces with a span in the window: by their duration,
// services, status, any span's attributes, root operation and span count.
func Search(spans []Span, r Request) (SearchResult, error) {
	result := SearchResult{Traces: []Summary{}, Services: []string{}, RootOperations: []OperationStats{}, Attributes: []AttributeCount{}}
	if err := r.validate(); err != nil {
		return result, err
	}
	grouped, err := groups(spans)
	if err != nil {
		return result, err
	}
	min, _ := duration(r.MinDuration)
	max, _ := duration(r.MaxDuration)
	services := map[string]bool{}
	facets := newFacets()
	for _, trace := range grouped {
		if !slices.ContainsFunc(trace, func(span Span) bool { return inWindow(span, r) }) {
			continue
		}
		summary, root := summarize(trace)
		for _, service := range summary.Services {
			services[service] = true
		}
		if !r.passes(summary, trace, min, max) {
			continue
		}
		facets.add(summary, root)
		if summary.SpanCount >= r.MinSpans && r.Operations.pass(func(operation string) bool { return operation == summary.Operation }) {
			result.Traces = append(result.Traces, summary)
		}
	}
	result.RootOperations, result.Attributes = facets.operations(), facets.attributes()
	slices.SortFunc(result.Traces, func(a, b Summary) int {
		if cmp := b.StartedAt.Compare(a.StartedAt); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.TraceID, b.TraceID)
	})
	result.Total = len(result.Traces)
	limit := r.Limit
	if limit == 0 {
		limit = 100
	}
	if len(result.Traces) > limit {
		result.Traces = result.Traces[:limit]
	}
	for key := range services {
		result.Services = append(result.Services, key)
	}
	slices.Sort(result.Services)
	return result, nil
}

func Trace(spans []Span, id string) (Detail, error) {
	grouped, err := groups(spans)
	if err != nil {
		return Detail{}, err
	}
	selected := grouped[id]
	if len(selected) == 0 {
		return Detail{}, fmt.Errorf("%w: %q", ErrTraceNotFound, id)
	}
	slices.SortFunc(selected, func(a, b Span) int {
		if cmp := a.StartedAt.Compare(b.StartedAt); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.SpanID, b.SpanID)
	})
	parents := map[string]string{}
	for _, span := range selected {
		parents[span.SpanID] = span.ParentID
	}
	missing := map[string]bool{}
	finished := map[string]bool{}
	for _, span := range selected {
		visited := map[string]bool{}
		for id := span.SpanID; id != ""; {
			if finished[id] {
				break
			}
			if visited[id] {
				return Detail{}, fmt.Errorf("parent cycle in trace %s at span %s", span.TraceID, id)
			}
			visited[id] = true
			parent, ok := parents[id]
			if !ok {
				missing[id] = true
				break
			}
			id = parent
		}
		for id := range visited {
			finished[id] = true
		}
	}
	summary, _ := summarize(selected)
	result := Detail{Summary: summary, Spans: selected, MissingParents: []string{}}
	for id := range missing {
		result.MissingParents = append(result.MissingParents, id)
	}
	slices.Sort(result.MissingParents)
	return result, nil
}
