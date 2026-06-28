package providers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

func init() {
	query.RegisterProvider(&jaegerProvider{})
}

// jaegerProvider queries the Jaeger HTTP query API and returns one row per span.
// Each row carries traceID/spanID/operationName/serviceName/startTime/duration and
// a `tags` map that merges the span's process (resource) tags with the span's own
// tags, so CEL columns can read e.g. row.tags['k8s.pod.name']. HAR capture is
// driven by the context's effective level (feature "jaeger").
type jaegerProvider struct{}

func (jaegerProvider) Type() string { return "jaeger" }

// jaegerOptions are decoded from ProviderRequest.Options. When the request Query
// is set it is treated as a trace ID (GET /api/traces/{id}); otherwise a search
// (GET /api/traces?service=...) is performed and Service is required.
type jaegerOptions struct {
	// URL is an inline Jaeger query base URL used when no stored connection is referenced.
	URL string `json:"url,omitempty"`

	Service     string `json:"service,omitempty"`
	Operation   string `json:"operation,omitempty"`
	Lookback    string `json:"lookback,omitempty"`
	Start       string `json:"start,omitempty"`
	End         string `json:"end,omitempty"`
	Limit       string `json:"limit,omitempty"`
	MinDuration string `json:"minDuration,omitempty"`
	MaxDuration string `json:"maxDuration,omitempty"`
	Tags        string `json:"tags,omitempty"`
}

func (jaegerProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	opts, err := query.DecodeOptions[jaegerOptions](req.Options)
	if err != nil {
		return nil, err
	}

	conn := connection.HTTPConnection{ConnectionName: req.Connection}
	hydrated, err := conn.Hydrate(ctx, ctx.GetNamespace())
	if err != nil {
		return nil, fmt.Errorf("failed to hydrate jaeger connection: %w", err)
	}

	base := opts.URL
	if base == "" {
		base = hydrated.URL
	}
	if base == "" {
		return nil, fmt.Errorf("jaeger query base url is required (set options.url or a connection)")
	}

	endpoint, err := jaegerEndpoint(base, strings.TrimSpace(req.Query), opts)
	if err != nil {
		return nil, err
	}

	client, err := connection.CreateHTTPClient(ctx, *hydrated)
	if err != nil {
		return nil, fmt.Errorf("failed to create http client: %w", err)
	}
	// Maintain HAR capture / HTTP logging driven by the context's effective level.
	connection.ApplyHTTPClientObservability(ctx, "jaeger", client, nil)

	resp, err := client.R(ctx).Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to execute jaeger request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if !resp.IsOK() {
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("jaeger request failed with status %d: %s", resp.StatusCode, string(peek))
	}

	maxBodySize := int64(ctx.Properties().Int(bodyMaxSizeProperty, defaultHTTPBodyMaxSizeBytes))
	if maxBodySize <= 0 {
		maxBodySize = defaultHTTPBodyMaxSizeBytes
	}
	body, err := readHTTPBodyWithLimit(resp.Body, resp.ContentLength, maxBodySize)
	if err != nil {
		return nil, err
	}

	return jaegerResultToRows(body)
}

// jaegerEndpoint builds the request URL: a single-trace fetch when traceID is set,
// otherwise a search (which requires a service).
func jaegerEndpoint(base, traceID string, opts jaegerOptions) (string, error) {
	base = strings.TrimRight(base, "/")
	if traceID != "" {
		return base + "/api/traces/" + url.PathEscape(traceID), nil
	}

	if opts.Service == "" {
		return "", fmt.Errorf("jaeger search requires options.service (or set the profile query to a trace id)")
	}

	q := url.Values{}
	q.Set("service", opts.Service)
	for key, val := range map[string]string{
		"operation":   opts.Operation,
		"lookback":    opts.Lookback,
		"start":       opts.Start,
		"end":         opts.End,
		"limit":       opts.Limit,
		"minDuration": opts.MinDuration,
		"maxDuration": opts.MaxDuration,
		"tags":        opts.Tags,
	} {
		if val != "" {
			q.Set(key, val)
		}
	}
	return base + "/api/traces?" + q.Encode(), nil
}

type jaegerTag struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

type jaegerSpan struct {
	TraceID       string      `json:"traceID"`
	SpanID        string      `json:"spanID"`
	OperationName string      `json:"operationName"`
	StartTime     int64       `json:"startTime"` // epoch microseconds
	Duration      int64       `json:"duration"`  // microseconds
	ProcessID     string      `json:"processID"`
	Tags          []jaegerTag `json:"tags"`
}

type jaegerProcess struct {
	ServiceName string      `json:"serviceName"`
	Tags        []jaegerTag `json:"tags"`
}

type jaegerTrace struct {
	Spans     []jaegerSpan             `json:"spans"`
	Processes map[string]jaegerProcess `json:"processes"`
}

type jaegerResponse struct {
	Data []jaegerTrace `json:"data"`
}

// jaegerResultToRows flattens the Jaeger response into one row per span, merging
// each span's process (resource) tags and its own tags into a single `tags` map
// (span tags win on key collision).
func jaegerResultToRows(body []byte) ([]query.Row, error) {
	var resp jaegerResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse jaeger response: %w", err)
	}

	rows := make([]query.Row, 0)
	for _, trace := range resp.Data {
		for _, span := range trace.Spans {
			proc := trace.Processes[span.ProcessID]
			tags := make(map[string]any, len(proc.Tags)+len(span.Tags))
			for _, t := range proc.Tags {
				tags[t.Key] = t.Value
			}
			for _, t := range span.Tags {
				tags[t.Key] = t.Value
			}
			rows = append(rows, query.Row{
				"traceID":       span.TraceID,
				"spanID":        span.SpanID,
				"operationName": span.OperationName,
				"serviceName":   proc.ServiceName,
				"startTime":     time.UnixMicro(span.StartTime).UTC(),
				"duration":      time.Duration(span.Duration) * time.Microsecond,
				"tags":          tags,
			})
		}
	}
	return rows, nil
}
