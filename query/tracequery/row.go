package tracequery

import (
	"github.com/flanksource/commons-db/otel/span"
	"github.com/flanksource/commons-db/query"
)

// FromRow projects a provider row through the shared canonical span boundary.
func FromRow(row query.Row) (Span, error) {
	normalized, err := span.Normalize(row, span.Options{})
	if err != nil {
		return normalized, err
	}
	_, err = groups([]Span{normalized})
	return normalized, err
}
