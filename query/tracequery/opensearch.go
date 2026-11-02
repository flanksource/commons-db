package tracequery

import (
	"context"
	"fmt"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
	"github.com/flanksource/commons-db/query/providers"
)

func ReadOpenSearch(queryContext dbcontext.Context, r Request, c *models.Connection, ids []string) ([]Span, error) {
	if c == nil {
		return nil, fmt.Errorf("source connection %q is required", r.Source)
	}
	if c.Type != models.ConnectionTypeOpenSearch && c.Type != models.ConnectionTypeOpenTelemetry {
		return nil, fmt.Errorf("source %q has unsupported type %q", r.Source, c.Type)
	}
	reference := r.Source
	options := map[string]any{}
	for _, key := range []string{"index", "format", "dateField", "traceIdField", "spanIdField", "parentIdField", "parentRefType", "serviceField", "operationField"} {
		if value := c.Properties[key]; value != "" {
			options[key] = value
		}
	}
	if r.Index != "" {
		options["index"] = r.Index
	}
	if r.Format != "" {
		options["format"] = string(r.Format)
	}
	format, _ := options["format"].(string)
	if format == "jaeger" {
		jaegerDefaults(options)
	}
	if len(ids) == 0 && r.TraceID != "" {
		ids = []string{r.TraceID}
	}
	if len(ids) > 0 {
		field := "trace_id"
		if value, ok := options["traceIdField"].(string); ok {
			field = value
		}
		values := make([]esdsl.Value, len(ids))
		for i, id := range ids {
			values[i] = *esdsl.Literal(id)
		}
		options["search"] = &esdsl.Search{Query: &esdsl.Condition{Op: "terms", Field: field, Values: values}}
	}
	return readOpenSearch(queryContext, providers.OpenTelemetryRead{Connection: reference, Options: options, From: r.From, To: r.Until})
}

func jaegerDefaults(options map[string]any) {
	for key, value := range map[string]string{"dateField": "startTimeMillis", "traceIdField": "traceID", "spanIdField": "spanID", "parentRefType": "CHILD_OF", "serviceField": "process.serviceName", "operationField": "operationName"} {
		if _, ok := options[key]; !ok {
			options[key] = value
		}
	}
}

func readOpenSearch(ctx dbcontext.Context, request providers.OpenTelemetryRead) ([]Span, error) {
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	spans := []Span{}
	var readErr error
	err := providers.ReadOpenTelemetrySpans(ctx.Wrap(readCtx), request, func(row query.Row) {
		if readErr != nil {
			return
		}
		converted, err := FromRow(row)
		if err != nil {
			readErr = err
			cancel()
			return
		}
		spans = append(spans, converted)
		if len(spans) > maxSpans {
			readErr = fmt.Errorf("OpenSearch read exceeds %d spans; narrow the time window or index", maxSpans)
			cancel()
		}
	})
	if readErr != nil {
		return nil, readErr
	}
	return spans, err
}
