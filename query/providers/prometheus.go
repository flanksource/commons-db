package providers

import (
	"fmt"
	"time"

	"github.com/flanksource/commons/duration"
	promAPI "github.com/prometheus/client_golang/api"
	promV1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"github.com/samber/lo"
	"github.com/timberio/go-datemath"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

func init() {
	query.RegisterProvider(&prometheusProvider{})
}

// prometheusProvider runs a PromQL instant or range query and returns one row
// per sample. Ported from duty/dataquery/prometheus.go.
type prometheusProvider struct{}

func (prometheusProvider) Type() string { return "prometheus" }

// prometheusOptions are decoded from ProviderRequest.Options.
type prometheusOptions struct {
	// URL is an inline Prometheus address used when no stored connection is referenced.
	URL string `json:"url,omitempty"`

	// Range, when set, runs a PromQL range query.
	Range *prometheusRange `json:"range,omitempty"`

	// SelectLabels restricts result labels to a deterministic subset.
	SelectLabels []string `json:"selectLabels,omitempty"`
}

type prometheusRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Step  string `json:"step"`
}

func (pr prometheusRange) toPrometheusRange(now time.Time) (promV1.Range, error) {
	if pr.Start == "" || pr.End == "" || pr.Step == "" {
		return promV1.Range{}, fmt.Errorf("prometheus range requires start, end and step")
	}

	start, err := datemath.ParseAndEvaluate(pr.Start, datemath.WithNow(now))
	if err != nil {
		return promV1.Range{}, fmt.Errorf("invalid prometheus range start time: %w", err)
	}
	end, err := datemath.ParseAndEvaluate(pr.End, datemath.WithNow(now))
	if err != nil {
		return promV1.Range{}, fmt.Errorf("invalid prometheus range end time: %w", err)
	}
	step, err := duration.ParseDuration(pr.Step)
	if err != nil {
		return promV1.Range{}, fmt.Errorf("invalid prometheus range step: %w", err)
	}

	stepDuration := time.Duration(step)
	if stepDuration <= 0 {
		return promV1.Range{}, fmt.Errorf("prometheus range step must be greater than zero")
	}
	if end.Before(start) {
		return promV1.Range{}, fmt.Errorf("prometheus range end time must be after start time")
	}

	return promV1.Range{Start: start, End: end, Step: stepDuration}, nil
}

func (prometheusProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("prometheus query is required")
	}

	opts, err := query.DecodeOptions[prometheusOptions](req.Options)
	if err != nil {
		return nil, err
	}

	conn := connection.PrometheusConnection{}
	conn.ConnectionName = req.Connection
	if opts.URL != "" {
		conn.URL = opts.URL
	}
	if err := conn.Populate(ctx); err != nil {
		return nil, fmt.Errorf("failed to populate prometheus connection: %w", err)
	}

	// Build the Prometheus client with a HAR-instrumented transport so requests
	// are captured under the "prometheus" feature.
	transport := connection.ApplyHTTPObservability(ctx, "prometheus", conn.Transport(), nil)
	apiClient, err := promAPI.NewClient(promAPI.Config{Address: conn.URL, RoundTripper: transport})
	if err != nil {
		return nil, fmt.Errorf("failed to create prometheus client: %w", err)
	}
	client := promV1.NewAPI(apiClient)

	result, err := runPromQL(ctx, client, req.Query, opts.Range)
	if err != nil {
		return nil, err
	}

	return transformPrometheusResult(result, opts.SelectLabels)
}

func runPromQL(ctx context.Context, client promV1.API, promQL string, r *prometheusRange) (model.Value, error) {
	if r != nil {
		promRange, err := r.toPrometheusRange(time.Now())
		if err != nil {
			return nil, err
		}

		result, warnings, err := client.QueryRange(ctx, promQL, promRange)
		if err != nil {
			return nil, fmt.Errorf("failed to execute PromQL range query: %w", err)
		}
		if len(warnings) > 0 {
			ctx.Warnf("Prometheus query warnings: %v", warnings)
		}
		return result, nil
	}

	result, warnings, err := client.Query(ctx, promQL, time.Now())
	if err != nil {
		return nil, fmt.Errorf("failed to execute PromQL query: %w", err)
	}
	if len(warnings) > 0 {
		ctx.Warnf("Prometheus query warnings: %v", warnings)
	}
	return result, nil
}

func rowFromMetric(metric model.Metric, selectLabels []string) query.Row {
	row := make(query.Row)
	for label, value := range metric {
		if len(selectLabels) == 0 || lo.Contains(selectLabels, string(label)) {
			row[string(label)] = string(value)
		}
	}
	return row
}

func transformPrometheusResult(result model.Value, selectLabels []string) ([]query.Row, error) {
	if result == nil {
		return []query.Row{}, nil
	}

	var results []query.Row
	switch v := result.(type) {
	case model.Vector:
		for _, sample := range v {
			row := rowFromMetric(sample.Metric, selectLabels)
			row["value"] = float64(sample.Value)
			results = append(results, row)
		}
	case model.Matrix:
		for _, sampleStream := range v {
			for _, samplePair := range sampleStream.Values {
				row := rowFromMetric(sampleStream.Metric, selectLabels)
				row["timestamp"] = samplePair.Timestamp.Time()
				row["value"] = float64(samplePair.Value)
				results = append(results, row)
			}
		}
	case *model.Scalar:
		results = append(results, query.Row{"value": float64(v.Value), "timestamp": v.Timestamp.Time()})
	case *model.String:
		results = append(results, query.Row{"value": v.Value})
	default:
		return nil, fmt.Errorf("unsupported prometheus result type: %T", result)
	}

	return results, nil
}
