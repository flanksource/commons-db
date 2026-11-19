package tracequery

import (
	"context"
	"fmt"
	"maps"
	"slices"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/otel/span/layout"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
	"github.com/flanksource/commons-db/query/providers"
)

// openSearchSource is a connection's span index, read through the span layout
// resolved from its mapping.
type openSearchSource struct {
	ctx          dbcontext.Context
	reference    string
	options      map[string]any
	search       esdsl.Search
	definition   layout.Definition
	traceIDField string
}

func resolveOpenSearchSource(queryContext dbcontext.Context, r Request, c *models.Connection) (openSearchSource, error) {
	if c == nil {
		return openSearchSource{}, fmt.Errorf("source connection %q is required", r.Source)
	}
	if c.Type != models.ConnectionTypeOpenSearch && c.Type != models.ConnectionTypeOpenTelemetry {
		return openSearchSource{}, fmt.Errorf("source %q has unsupported type %q", r.Source, c.Type)
	}
	// The connection supplies index and format, which the request may override;
	// the trace, span and date fields come from the index's span layout.
	options := map[string]any{}
	if r.Index != "" {
		options["index"] = r.Index
	}
	if r.Format != "" {
		options["format"] = string(r.Format)
	}
	definition, err := providers.ResolveSpanLayout(queryContext, providers.SpanLayoutRequest{Connection: r.Source, Options: options})
	if err != nil {
		return openSearchSource{}, fmt.Errorf("source %q: %w", r.Source, err)
	}
	traceIDField, err := definition.SourceField("trace_id")
	if err != nil {
		return openSearchSource{}, fmt.Errorf("source %q: %w", r.Source, err)
	}
	spanIDField, err := definition.SourceField("span_id")
	if err != nil {
		return openSearchSource{}, fmt.Errorf("source %q: %w", r.Source, err)
	}
	clock := definition.Fields["timestamp"]
	options["dateField"], options["traceIdField"], options["spanIdField"] = clock.Source, traceIDField, spanIDField
	// The service a row reports is the one a pushed service filter matched on.
	if service, ok := definition.Fields["service"]; ok {
		options["serviceField"] = service.Source
	}
	return openSearchSource{
		ctx: queryContext, reference: r.Source, options: options,
		// A numeric timestamp bounds a read only once its epoch unit is declared.
		search:     esdsl.Search{TimeFieldFormat: clock.TimeFieldFormat()},
		definition: definition, traceIDField: traceIDField,
	}, nil
}

// read is the spans meeting query, or every span when it is nil, that start
// between from and until; an empty bound leaves that side open.
func (s openSearchSource) read(query *esdsl.Condition, from, until string) ([]Span, error) {
	options := maps.Clone(s.options)
	search := s.search
	search.Query = query
	options["search"] = &search
	return readOpenSearch(s.ctx, providers.OpenTelemetryRead{Connection: s.reference, Options: options, From: from, To: until})
}

// ReadOpenSearch reads the spans of the traces ids, or of r.TraceID, or else
// every span in r's window.
func ReadOpenSearch(queryContext dbcontext.Context, r Request, c *models.Connection, ids []string) ([]Span, error) {
	source, err := resolveOpenSearchSource(queryContext, r, c)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 && r.TraceID != "" {
		ids = []string{r.TraceID}
	}
	if len(ids) == 0 {
		return source.read(nil, r.From, r.Until)
	}
	byID := termsCondition(source.traceIDField, ids)
	return source.read(&byID, r.From, r.Until)
}

// SearchOpenSearch searches the traces with a span in r's window. Every span
// the window holds settles which traces those are; the filters the span layout
// matches exactly then narrow them across whole traces, so a filter met
// outside the window still counts, and only the traces that may pass are read
// in full. The services facet still covers every trace in the window.
func SearchOpenSearch(queryContext dbcontext.Context, r Request, c *models.Connection) (SearchResult, error) {
	source, err := resolveOpenSearchSource(queryContext, r, c)
	if err != nil {
		return SearchResult{}, err
	}
	window, err := source.read(nil, r.From, r.Until)
	if err != nil {
		return SearchResult{}, err
	}
	window = slices.DeleteFunc(window, func(span Span) bool { return !inWindow(span, r) })
	ids := traceIDs(window)
	candidates, err := source.narrow(pushdown(r, source.definition), ids)
	if err != nil {
		return SearchResult{}, err
	}
	spans := []Span{}
	if len(candidates) > 0 {
		byID := termsCondition(source.traceIDField, candidates)
		if spans, err = source.read(&byID, "", ""); err != nil {
			return SearchResult{}, err
		}
	}
	services := spanServices(slices.Concat(window, spans))
	kept := map[string]bool{}
	for _, id := range candidates {
		kept[id] = true
	}
	if others := slices.DeleteFunc(ids, func(id string) bool { return kept[id] }); len(others) > 0 {
		unseen := source.unseenServices(others, services)
		rest, err := source.read(&unseen, "", "")
		if err != nil {
			return SearchResult{}, err
		}
		maps.Copy(services, spanServices(rest))
	}
	result, err := Search(spans, r)
	if err != nil {
		return result, err
	}
	result.Services = slices.AppendSeq([]string{}, maps.Keys(services))
	slices.Sort(result.Services)
	return result, nil
}

// unseenServices is the spans of the traces ids whose service is none of
// known: all the services of those traces add. Without a keyword service field
// it is every span of them.
func (s openSearchSource) unseenServices(ids []string, known map[string]bool) esdsl.Condition {
	condition := esdsl.Condition{Op: esdsl.OpBool, Conditions: []esdsl.Condition{termsCondition(s.traceIDField, ids)}}
	if field, ok := keywordField(s.definition, "service"); ok {
		seen := termsCondition(field, slices.Sorted(maps.Keys(known)))
		seen.Occur = esdsl.OccurMustNot
		condition.Conditions = append(condition.Conditions, seen)
	}
	return condition
}

func spanServices(spans []Span) map[string]bool {
	services := map[string]bool{}
	for _, span := range spans {
		services[span.Service] = true
	}
	return services
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
