// Package opensearchtraces is the opensearch trace kind: span documents of an
// opentelemetry connection's index, imported over a window or followed.
package opensearchtraces

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
	"github.com/flanksource/commons-db/query/providers"
	"github.com/flanksource/commons-db/recordstore/recordresults"

	"github.com/flanksource/commons-db/tracing/traces"
)

// Params choose the index and the window a capture reads.
type Params struct {
	// Connection is the opentelemetry connection whose OpenSearch index holds
	// the spans.
	Connection string `json:"connection" clicky:"required,title=Connection"`
	// Index, Format and DateField override the connection's defaults:
	// otel-traces-*, flat, and @timestamp.
	Index     string `json:"index,omitempty" clicky:"title=Index"`
	Format    string `json:"format,omitempty" clicky:"title=Format"`
	DateField string `json:"dateField,omitempty" clicky:"title=Date field"`
	// Search narrows the spans read.
	Search *esdsl.Search `json:"search,omitempty"`
	// From and To bound the window, as date math such as now-1h or RFC3339.
	From string `json:"from,omitempty" clicky:"title=From"`
	To   string `json:"to,omitempty" clicky:"title=To"`
	// Follow goes on reading the spans indexed after the window, until the
	// session stops; it has no To.
	Follow bool `json:"follow,omitempty" clicky:"title=Follow"`
	// Poll and Lag, durations such as 2s, pace a follow and hold it behind now.
	Poll string `json:"poll,omitempty" clicky:"title=Poll"`
	Lag  string `json:"lag,omitempty" clicky:"title=Lag"`
}

func (p Params) Validate() error {
	if strings.TrimSpace(p.Connection) == "" {
		return errors.New("connection is required: the opentelemetry connection whose index holds the spans")
	}
	if p.Follow && p.To != "" {
		return errors.New("a follow has no end: drop to, or read the window without follow")
	}
	if p.Format != "" && p.Format != "flat" && p.Format != "jaeger" {
		return fmt.Errorf("format %q is neither flat nor jaeger", p.Format)
	}
	_, _, err := p.pacing()
	return err
}

func (p Params) pacing() (poll, lag time.Duration, err error) {
	for _, option := range []struct {
		name, value string
		into        *time.Duration
	}{{"poll", p.Poll, &poll}, {"lag", p.Lag, &lag}} {
		if option.value == "" {
			continue
		}
		parsed, err := time.ParseDuration(option.value)
		if err != nil || parsed < 0 {
			return 0, 0, fmt.Errorf("%s %q is not a duration such as 2s", option.name, option.value)
		}
		*option.into = parsed
	}
	return poll, lag, nil
}

func (p Params) providerOptions() map[string]any {
	options := map[string]any{}
	for name, value := range map[string]string{"index": p.Index, "format": p.Format, "dateField": p.DateField} {
		if value != "" {
			options[name] = value
		}
	}
	if p.Search != nil {
		options["search"] = p.Search
	}
	return options
}

// Span is one span document as a capture stores it. SourceKey, its index and
// id, keys it: a span read twice, by overlapping reads, is stored once.
type Span struct {
	Timestamp   time.Time      `json:"timestamp" pretty:"label=Time" sort:"timestamp"`
	TraceID     string         `json:"traceId" pretty:"label=Trace" filter:"exact"`
	SpanID      string         `json:"spanId" pretty:"label=Span" filter:"exact"`
	ParentID    string         `json:"parentId,omitempty" pretty:"label=Parent" filter:"exact"`
	Service     string         `json:"service" filter:"terms"`
	Operation   string         `json:"operation" filter:"terms"`
	Status      string         `json:"status,omitempty" filter:"terms"`
	DurationMs  float64        `json:"durationMs" pretty:"label=Duration,type=duration,unit=ms" sort:"durationMs"`
	Attributes  map[string]any `json:"attributes,omitempty" pretty:"hide"`
	SourceIndex string         `json:"sourceIndex" pretty:"hide"`
	SourceID    string         `json:"sourceId" pretty:"hide"`
	SourceKey   string         `json:"sourceKey" pretty:"hide"`
}

// FromRow is the span an opentelemetry row describes.
func FromRow(row query.Row) Span {
	text := func(key string) string {
		value, _ := row[key].(string)
		return value
	}
	span := Span{
		TraceID: text("trace_id"), SpanID: text("span_id"), ParentID: text("parent_id"),
		Service: text("service"), Operation: text("operation"), Status: text("status"),
		SourceIndex: text("source_index"), SourceID: text("source_id"),
	}
	span.Timestamp, _ = time.Parse(time.RFC3339Nano, text("timestamp"))
	span.DurationMs, _ = row["duration_ms"].(float64)
	span.Attributes, _ = row["_attributes"].(map[string]any)
	span.SourceKey = span.SourceIndex + "/" + span.SourceID
	return span
}

type spans struct{}

func (spans) Params() Params { return Params{} }

func (spans) Schema() recordresults.ResultType[Span] {
	return recordresults.ResultType[Span]{Title: "OpenSearch traces", TimeColumn: "timestamp", KeyColumn: "sourceKey"}
}

// Handle reads the window, and with Follow every span indexed after it,
// emitting each span it reads in order.
func (spans) Handle(ctx dbcontext.Context, params Params, records traces.Emitter[Span], _ traces.Records[Span]) error {
	poll, lag, err := params.pacing()
	if err != nil {
		return err
	}
	emitCtx := context.WithoutCancel(ctx)
	var emitErr error
	readErr := providers.ReadOpenTelemetrySpans(ctx, providers.OpenTelemetryRead{
		Connection: params.Connection, Options: params.providerOptions(),
		From: params.From, To: params.To, Follow: params.Follow, Poll: poll, Lag: lag,
	}, func(row query.Row) {
		if err := records.Emit(emitCtx, FromRow(row)); err != nil && emitErr == nil {
			emitErr = err
		}
	})
	return errors.Join(readErr, emitErr)
}

// Kind is the opensearch trace kind: spans masked as they are stored.
func Kind() *traces.Handler[Params, Span] {
	return traces.NewHandler[Params, Span](spans{}, traces.Capabilities{Historical: true, Follow: true}).
		WithSecretMasking()
}
