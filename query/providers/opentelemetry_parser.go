package providers

import (
	"github.com/flanksource/commons-db/otel/span"
	"github.com/flanksource/commons-db/query"
)

func openTelemetryRow(document map[string]any, options OpenTelemetryOptions) (query.Row, error) {
	if options.RawRows {
		return query.Row(document), nil
	}
	normalized, err := span.Normalize(document, span.Options{Format: options.Format, DateField: options.DateField})
	if err != nil {
		return nil, err
	}
	row := query.Row(document)
	for key, value := range normalized.Row() {
		row[key] = value
	}
	row["id"], row["service_name"], row["operation_name"] = normalized.SpanID, normalized.Service, normalized.Operation
	row["_trace"] = normalized
	for key, value := range normalized.Attributes {
		if _, exists := row[key]; !exists {
			row[key] = value
		}
	}
	return row, nil
}
