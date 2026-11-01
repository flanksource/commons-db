package providers

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/metrics"
	"github.com/prometheus/common/model"
)

// LookupFilterValues lists a label's values through /api/v1/label/<name>/values.
// A metric SQL column is mapped onto its upstream label by Profile.Labels and
// scoped to the metric tables the statement reads; a PromQL row's columns are
// the labels themselves, unscoped because a PromQL expression is not a selector.
func (prometheusProvider) LookupFilterValues(ctx context.Context, req query.ProviderRequest, lookup query.FilterValueSearch) ([]query.FilterOption, *query.Total, error) {
	client, opts, err := newPrometheusClient(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	label, matches, err := prometheusLookupScope(req, opts, lookup.Binding.Field)
	if err != nil {
		return nil, nil, err
	}
	var start, end time.Time
	if opts.Range != nil {
		r, err := opts.Range.toPrometheusRange(time.Now())
		if err != nil {
			return nil, nil, err
		}
		start, end = r.Start, r.End
	}
	values, warnings, err := client.LabelValues(ctx, label, matches, start, end)
	if err != nil {
		return nil, nil, fmt.Errorf("list Prometheus label %q values: %w", label, err)
	}
	if len(warnings) > 0 {
		return nil, nil, fmt.Errorf("prometheus returned incomplete label values: %v", warnings)
	}
	options := []query.FilterOption{}
	for _, value := range values {
		if lookup.Search == "" || strings.Contains(strings.ToLower(string(value)), strings.ToLower(lookup.Search)) {
			options = append(options, query.FilterOption{Value: string(value)})
		}
	}
	sort.Slice(options, func(i, j int) bool { return options[i].Value < options[j].Value })
	total := &query.Total{Value: int64(len(options)), Exact: true}
	if lookup.Limit > 0 && len(options) > lookup.Limit {
		options = options[:lookup.Limit]
	}
	return options, total, nil
}

func prometheusLookupScope(req query.ProviderRequest, opts PrometheusOptions, field string) (string, []string, error) {
	label := field
	var matches []string
	if opts.Language == "sql" {
		mapped, ok := req.Labels[field]
		if !ok {
			return "", nil, fmt.Errorf("column %q is not a metric label: declare it in the profile's labels", field)
		}
		label = mapped
		plan, err := metrics.Plan(req.Query)
		if err != nil {
			return "", nil, err
		}
		for _, source := range plan.Sources {
			matches = append(matches, fmt.Sprintf("{%s=%q}", model.MetricNameLabel, source.Name))
		}
	}
	if !model.LegacyValidation.IsValidLabelName(label) {
		return "", nil, fmt.Errorf("unsupported Prometheus label name %q", label)
	}
	return label, matches, nil
}
