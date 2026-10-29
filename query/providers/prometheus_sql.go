package providers

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/metrics"
	promV1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

func executePrometheusSQL(ctx context.Context, req query.ProviderRequest, client promV1.API, opts prometheusOptions) ([]query.Row, error) {
	if opts.Range == nil {
		return nil, fmt.Errorf("prometheus SQL requires a range")
	}
	r, err := opts.Range.toPrometheusRange(time.Now())
	if err != nil {
		return nil, err
	}
	return metrics.Execute(ctx, metrics.Request{SQL: req.Query, Args: req.QueryArgs, Labels: req.Labels}, func(s metrics.Source, emit func(metrics.Sample) error) error {
		matchers := []string{fmt.Sprintf("%s=%q", model.MetricNameLabel, s.Name)}
		for alias, value := range s.Filters {
			if label, ok := req.Labels[alias]; ok {
				if !model.LabelName(label).IsValid() {
					return fmt.Errorf("unsupported Prometheus label name %q", label)
				}
				matchers = append(matchers, fmt.Sprintf("%s=%q", label, value))
			}
		}
		sort.Strings(matchers)
		expression := "{" + strings.Join(matchers, ",") + "}"
		if s.Transform != "" {
			if _, err := model.ParseDuration(s.Window); err != nil {
				return fmt.Errorf("invalid counter window: %w", err)
			}
			expression = fmt.Sprintf("%s(%s[%s])", s.Transform, expression, s.Window)
		}
		samples := 0
		for start := r.Start; !start.After(r.End); {
			steps := min(int64(r.End.Sub(start)/r.Step), int64(9999))
			end := start.Add(time.Duration(steps) * r.Step)
			count, err := readPrometheusSamples(ctx, client, expression, promV1.Range{Start: start, End: end, Step: r.Step}, emit)
			if err != nil {
				return err
			}
			samples += count
			if end.Equal(r.End) || r.End.Sub(end) < r.Step {
				break
			}
			start = end.Add(r.Step)
		}
		if req.MetricMetadata != nil {
			req.MetricMetadata.Sources = append(req.MetricMetadata.Sources, query.MetricSource{Expression: expression, Start: r.Start, End: r.End, Step: r.Step.String(), Samples: samples})
		}
		return nil
	})
}

func readPrometheusSamples(ctx context.Context, client promV1.API, expression string, r promV1.Range, emit func(metrics.Sample) error) (int, error) {
	result, warnings, err := client.QueryRange(ctx, expression, r)
	if err != nil {
		return 0, err
	}
	if len(warnings) > 0 {
		return 0, fmt.Errorf("prometheus returned incomplete/annotated data: %v", warnings)
	}
	matrix, ok := result.(model.Matrix)
	if !ok {
		return 0, fmt.Errorf("expected metric matrix, got %T", result)
	}
	samples := 0
	for _, stream := range matrix {
		if len(stream.Histograms) > 0 {
			return 0, fmt.Errorf("raw native histograms require native PromQL reduction")
		}
		labels := map[string]string{}
		for k, v := range stream.Metric {
			labels[string(k)] = string(v)
		}
		for _, pair := range stream.Values {
			if err := emit(metrics.Sample{Time: pair.Timestamp.Time(), Value: float64(pair.Value), Labels: labels}); err != nil {
				return 0, err
			}
			samples++
		}
	}
	return samples, nil
}
