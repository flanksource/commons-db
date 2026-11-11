package tracequery

import (
	"cmp"
	"fmt"
	"slices"
)

const (
	maxAttributeSuggestions = 100
	maxAttributeValueLength = 100
)

// facets gather what a search's traces hold, for its filters to offer: the
// traces of each root operation and the values of their root spans' attributes.
type facets struct {
	byOperation map[string][]Summary
	byAttribute map[string]int
}

func newFacets() *facets {
	return &facets{byOperation: map[string][]Summary{}, byAttribute: map[string]int{}}
}

func (f *facets) add(summary Summary, root Span) {
	f.byOperation[summary.Operation] = append(f.byOperation[summary.Operation], summary)
	seen := map[string]bool{}
	for _, values := range []map[string]any{root.Attributes, root.Resource} {
		for key, value := range values {
			switch value.(type) {
			case string, bool, int, int32, int64, uint64, float32, float64:
			default:
				continue
			}
			text := fmt.Sprint(value)
			if len(text) > maxAttributeValueLength || seen[key] {
				continue
			}
			seen[key] = true
			f.byAttribute[key+"="+text]++
		}
	}
}

// operations are the root operations, those of multi-span traces first, then the most frequent.
func (f *facets) operations() []OperationStats {
	result := []OperationStats{}
	for operation, traces := range f.byOperation {
		durations := []float64{}
		single := true
		for _, trace := range traces {
			durations = append(durations, trace.DurationMs)
			single = single && trace.SpanCount == 1
		}
		slices.Sort(durations)
		result = append(result, OperationStats{Operation: operation, Count: len(traces), SingleSpan: single, P50Ms: quantile(durations, 0.5), P95Ms: quantile(durations, 0.95)})
	}
	slices.SortFunc(result, func(a, b OperationStats) int {
		if a.SingleSpan != b.SingleSpan {
			if a.SingleSpan {
				return 1
			}
			return -1
		}
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Operation, b.Operation))
	})
	return result
}

// attributes are the most frequent root span attribute values, key=value.
func (f *facets) attributes() []AttributeCount {
	result := []AttributeCount{}
	for value, count := range f.byAttribute {
		result = append(result, AttributeCount{Value: value, Count: count})
	}
	slices.SortFunc(result, func(a, b AttributeCount) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Value, b.Value))
	})
	return result[:min(len(result), maxAttributeSuggestions)]
}

// quantile is the q-quantile of sorted values, the nearest rank at or below it.
func quantile(sorted []float64, q float64) float64 {
	return sorted[min(len(sorted)-1, int(q*float64(len(sorted))))]
}
