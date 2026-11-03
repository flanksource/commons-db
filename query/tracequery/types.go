package tracequery

import (
	"context"
	"errors"
	"time"

	"github.com/flanksource/commons-db/connection/store"
	"github.com/flanksource/commons-db/otel/span"
)

var ErrTraceNotFound = errors.New("trace not found")

type DocumentFormat string

func (DocumentFormat) JSONSchema() map[string]any {
	return map[string]any{"type": "string", "enum": []string{"flat", "jaeger"}}
}

type Span = span.Span

type Request struct {
	Source      string         `json:"source" clicky:"title=Source,required"`
	Run         string         `json:"run,omitempty" clicky:"title=Run,desc=Optional recorded run ID"`
	Index       string         `json:"index,omitempty" clicky:"title=Index,desc=OpenSearch index pattern (default otel-traces-*)"`
	Format      DocumentFormat `json:"format,omitempty" clicky:"title=Document format"`
	Services    Modes          `json:"services,omitempty" clicky:"title=Services,desc=Services a trace's spans run in: each included or excluded"`
	Operations  Modes          `json:"operations,omitempty" clicky:"title=Root operations,desc=A trace's root operation: each included or excluded"`
	Statuses    Modes          `json:"statuses,omitempty" clicky:"title=Statuses,desc=error (a span failed) or ok: each included or excluded"`
	Attributes  Modes          `json:"attributes,omitempty" clicky:"title=Attributes,desc=Span or resource attributes of any span: key, key=value or key=prefix*, each included or excluded"`
	MinSpans    int            `json:"minSpans,omitempty" clicky:"title=Minimum spans,desc=Traces of at least this many spans"`
	TraceID     string         `json:"traceId,omitempty" clicky:"title=Trace ID"`
	Lookback    string         `json:"lookback,omitempty" clicky:"title=Lookback,desc=Duration such as 15m or 24h; used when From is empty"`
	From        string         `json:"from,omitempty" clicky:"title=From,desc=Inclusive RFC3339 timestamp"`
	Until       string         `json:"until,omitempty" clicky:"title=Until,desc=Exclusive RFC3339 timestamp"`
	MinDuration string         `json:"minDuration,omitempty" clicky:"title=Minimum trace duration,desc=Duration such as 100ms or 2s"`
	MaxDuration string         `json:"maxDuration,omitempty" clicky:"title=Maximum trace duration,desc=Duration such as 100ms or 2s"`
	Limit       int            `json:"limit,omitempty" clicky:"title=Trace limit,desc=Maximum traces (1 to 1000); spans of a trace stay together"`
}

type Source struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}
type Summary struct {
	TraceID    string    `json:"traceId"`
	Operation  string    `json:"operation"`
	Service    string    `json:"service"`
	StartedAt  time.Time `json:"startedAt"`
	DurationMs float64   `json:"durationMs"`
	SpanCount  int       `json:"spanCount"`
	Errors     int       `json:"errors"`
	Services   []string  `json:"services"`
}
type SearchResult struct {
	Traces []Summary `json:"traces"`
	Total  int       `json:"total"`
	// Services are those of every trace in the window.
	Services []string `json:"services"`
	// RootOperations count the traces in the window that pass every filter
	// but the operation and span-count ones, by root operation.
	RootOperations []OperationStats `json:"rootOperations"`
	// Attributes suggest values of those traces' root spans, most frequent first.
	Attributes []AttributeCount `json:"attributes"`
}

// OperationStats are the traces of one root operation.
type OperationStats struct {
	Operation string `json:"operation"`
	Count     int    `json:"count"`
	// SingleSpan is set when every one of them is a single span.
	SingleSpan bool    `json:"singleSpan,omitempty"`
	P50Ms      float64 `json:"p50Ms"`
	P95Ms      float64 `json:"p95Ms"`
}

// AttributeCount is how many traces' root spans carry key=value.
type AttributeCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}
type Detail struct {
	Summary        Summary  `json:"summary"`
	Spans          []Span   `json:"spans"`
	MissingParents []string `json:"missingParents"`
}

type Service struct {
	Connections *store.Store
	Read        func(context.Context, Request, []string) ([]Span, error)
}
