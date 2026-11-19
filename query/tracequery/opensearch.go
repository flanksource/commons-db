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
	// The connection supplies index and format, which the request may override;
	// the trace, service and date fields come from the index's span layout.
	options := map[string]any{}
	if r.Index != "" {
		options["index"] = r.Index
	}
	if r.Format != "" {
		options["format"] = string(r.Format)
	}
	definition, err := providers.ResolveSpanLayout(queryContext, providers.SpanLayoutRequest{Connection: r.Source, Options: options})
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", r.Source, err)
	}
	traceIDField, err := definition.SourceField("trace_id")
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", r.Source, err)
	}
	options["dateField"] = definition.Fields["timestamp"].Source
	if len(ids) == 0 && r.TraceID != "" {
		ids = []string{r.TraceID}
	}
	read := func(query *esdsl.Condition) ([]Span, error) {
		if query != nil {
			options["search"] = &esdsl.Search{Query: query}
		}
		return readOpenSearch(queryContext, providers.OpenTelemetryRead{Connection: r.Source, Options: options, From: r.From, To: r.Until})
	}
	if len(ids) > 0 {
		byID := termsCondition(traceIDField, ids)
		return read(&byID)
	}
	return readNarrowed(read, pushdown(r, definition), traceIDField)
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
