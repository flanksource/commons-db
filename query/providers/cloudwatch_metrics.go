package providers

import (
	stdctx "context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cw "github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/metrics"
	"golang.org/x/sync/errgroup"
)

func init() { query.RegisterProvider(cloudwatchMetricsProvider{}) }

type cloudwatchMetricsProvider struct{}

func (cloudwatchMetricsProvider) Type() string { return "cloudwatchmetrics" }

type cloudwatchMetric struct {
	Name       string            `json:"name"`
	Namespace  string            `json:"namespace"`
	Dimensions map[string]string `json:"dimensions"`
	Statistic  string            `json:"statistic"`
	Unit       string            `json:"unit"`
}
type cloudwatchMetricsOptions struct {
	Language    string             `json:"language"`
	Region      string             `json:"region"`
	Endpoint    string             `json:"endpoint"`
	Namespace   string             `json:"namespace"`
	Statistic   string             `json:"statistic"`
	Unit        string             `json:"unit"`
	Range       *prometheusRange   `json:"range"`
	Metrics     []cloudwatchMetric `json:"metrics"`
	Dimensions  map[string]string  `json:"dimensions"`
	Concurrency int                `json:"concurrency"`
}

func (cloudwatchMetricsProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	opts, err := query.DecodeOptions[cloudwatchMetricsOptions](req.Options)
	if err != nil {
		return nil, err
	}
	if opts.Range == nil {
		return nil, fmt.Errorf("cloudwatchmetrics requires range")
	}
	r, err := opts.Range.toPrometheusRange(time.Now())
	if err != nil {
		return nil, err
	}
	if r.Step%time.Second != 0 {
		return nil, fmt.Errorf("CloudWatch period must be whole seconds")
	}
	if !r.End.After(r.Start) {
		return nil, fmt.Errorf("CloudWatch range end must be after start")
	}
	period := int32(r.Step / time.Second)
	age := time.Since(r.Start)
	if age > 455*24*time.Hour {
		return nil, fmt.Errorf("CloudWatch range exceeds 455 day metric retention")
	}
	multiple := int32(60)
	if age > 63*24*time.Hour {
		multiple = 3600
	} else if age > 15*24*time.Hour {
		multiple = 300
	}
	if period < 60 {
		if age > 3*time.Hour || period != 1 && period != 5 && period != 10 && period != 20 && period != 30 {
			return nil, fmt.Errorf("invalid high resolution CloudWatch range/period")
		}
	} else if period%multiple != 0 {
		return nil, fmt.Errorf("CloudWatch period must be a multiple of %d seconds for this range", multiple)
	}
	cfg, endpoint, err := awsConfigForRequest(ctx, req.Connection, opts.Region, opts.Endpoint)
	if err != nil {
		return nil, err
	}
	client := cw.NewFromConfig(cfg, func(o *cw.Options) {
		if endpoint != "" {
			o.BaseEndpoint = &endpoint
		}
	})
	if opts.Statistic == "" {
		opts.Statistic = "Average"
	}
	if opts.Language == "metrics-insights" && (req.Query == "" || age > 14*24*time.Hour || period < 60) {
		return nil, fmt.Errorf("Metrics Insights requires a query, at most two weeks of history and period >= 60s")
	}
	if opts.Concurrency == 0 {
		opts.Concurrency = 4
	}
	if opts.Concurrency < 1 || opts.Concurrency > 32 {
		return nil, fmt.Errorf("CloudWatch concurrency must be in 1..32")
	}
	fetch := func(s metrics.Source, emit func(metrics.Sample) error) error {
		samples := 0
		statistics := map[string]bool{}
		consume := func(sample metrics.Sample) error {
			samples++
			if sample.Statistic != "" {
				statistics[sample.Statistic] = true
			}
			return emit(sample)
		}
		if s.Transform != "" {
			return fmt.Errorf("counter expressions require prometheus")
		}
		identities := opts.Metrics
		if s.Name != "" && len(opts.Metrics) > 0 {
			identities = nil
			for _, m := range opts.Metrics {
				if m.Name == s.Name && (opts.Namespace == "" || m.Namespace == opts.Namespace) {
					identities = append(identities, m)
				}
			}
		}
		if s.Name != "" && len(opts.Metrics) == 0 {
			identities = nil
			if opts.Namespace == "" {
				return fmt.Errorf("CloudWatch SQL requires namespace")
			}
			input := &cw.ListMetricsInput{Namespace: &opts.Namespace, MetricName: &s.Name}
			if req.MetricMetadata != nil {
				req.MetricMetadata.Notices = append(req.MetricMetadata.Notices, query.QueryNotice{Severity: "warning", Message: "ListMetrics discovery omits inactive and recently created metrics; configure explicit metric identities for historical completeness"})
			}
			for name, value := range opts.Dimensions {
				input.Dimensions = append(input.Dimensions, types.DimensionFilter{Name: aws.String(name), Value: aws.String(value)})
			}
			for alias, value := range s.Filters {
				if dimension, ok := req.Labels[alias]; ok {
					input.Dimensions = append(input.Dimensions, types.DimensionFilter{Name: aws.String(dimension), Value: aws.String(value)})
				}
			}
			pager := cw.NewListMetricsPaginator(client, input)
			for pager.HasMorePages() {
				page, err := pager.NextPage(ctx)
				if err != nil {
					return err
				}
				for _, m := range page.Metrics {
					dims := map[string]string{}
					for _, d := range m.Dimensions {
						dims[aws.ToString(d.Name)] = aws.ToString(d.Value)
					}
					identities = append(identities, cloudwatchMetric{Name: aws.ToString(m.MetricName), Namespace: aws.ToString(m.Namespace), Dimensions: dims})
				}
				queries, err := cloudwatchQueries(identities, cloudwatchQueryDefaults{Statistic: opts.Statistic, Unit: opts.Unit, Period: period})
				if err != nil {
					return err
				}
				if err := readCloudwatch(ctx, client, queries, r.Start, r.End, consume); err != nil {
					return err
				}
				identities = nil
			}
		}
		queries, err := cloudwatchQueries(identities, cloudwatchQueryDefaults{Statistic: opts.Statistic, Unit: opts.Unit, Period: period})
		if err != nil {
			return err
		}
		if opts.Language == "metrics-insights" {
			queries = []types.MetricDataQuery{{Id: aws.String("insights"), Expression: &req.Query, Period: &period}}
		}
		if len(queries) == 0 && s.Name == "" {
			return fmt.Errorf("cloudwatchmetrics requires explicit metrics or a Metrics Insights query")
		}
		group, groupCtx := errgroup.WithContext(ctx)
		group.SetLimit(opts.Concurrency)
		var mu sync.Mutex
		for offset := 0; offset < len(queries); offset += 500 {
			batch := queries[offset:min(offset+500, len(queries))]
			group.Go(func() error {
				return readCloudwatch(groupCtx, client, batch, r.Start, r.End, func(sample metrics.Sample) error { mu.Lock(); defer mu.Unlock(); return consume(sample) })
			})
		}
		if err := group.Wait(); err != nil {
			return err
		}
		if req.MetricMetadata != nil {
			stats := []string{}
			for name := range statistics {
				stats = append(stats, name)
			}
			sort.Strings(stats)
			expression := opts.Namespace + "/" + s.Name
			if s.Name == "" {
				expression = "CloudWatch metric batch"
			}
			if opts.Language == "metrics-insights" {
				expression = req.Query
			}
			req.MetricMetadata.Sources = append(req.MetricMetadata.Sources, query.MetricSource{Expression: expression, Statistics: stats, Start: r.Start, End: r.End, Step: r.Step.String(), Samples: samples})
		}
		return nil
	}
	switch opts.Language {
	case "sql":
		return metrics.Execute(ctx, metrics.Request{SQL: req.Query, Args: req.QueryArgs, Labels: req.Labels}, fetch)
	case "", "metrics", "metrics-insights":
		rows := []query.Row{}
		err := fetch(metrics.Source{}, func(s metrics.Sample) error {
			row := query.Row{"time": s.Time, "value": s.Value, "metric_name": s.Metric, "metric_namespace": s.Namespace, "statistic": s.Statistic, "unit": s.Unit}
			for k, v := range s.Labels {
				row[k] = v
			}
			rows = append(rows, row)
			return nil
		})
		return rows, err
	default:
		return nil, fmt.Errorf("unsupported cloudwatchmetrics language %q", opts.Language)
	}
}

type cloudwatchQueryDefaults struct {
	Statistic, Unit string
	Period          int32
}

func cloudwatchQueries(identities []cloudwatchMetric, defaults cloudwatchQueryDefaults) ([]types.MetricDataQuery, error) {
	queries := make([]types.MetricDataQuery, 0, len(identities))
	for i, m := range identities {
		if m.Name == "" || m.Namespace == "" {
			return nil, fmt.Errorf("metric identity requires name and namespace")
		}
		stat := m.Statistic
		if stat == "" {
			stat = defaults.Statistic
		}
		unit := m.Unit
		if unit == "" {
			unit = defaults.Unit
		}
		if unit != "" && !slices.Contains(types.StandardUnit("").Values(), types.StandardUnit(unit)) {
			return nil, fmt.Errorf("invalid CloudWatch unit %q", unit)
		}
		dims := []types.Dimension{}
		for k, v := range m.Dimensions {
			dims = append(dims, types.Dimension{Name: aws.String(k), Value: aws.String(v)})
		}
		sort.Slice(dims, func(i, j int) bool { return aws.ToString(dims[i].Name) < aws.ToString(dims[j].Name) })
		queries = append(queries, types.MetricDataQuery{Id: aws.String(fmt.Sprintf("m%d", i)), MetricStat: &types.MetricStat{Metric: &types.Metric{MetricName: &m.Name, Namespace: &m.Namespace, Dimensions: dims}, Stat: &stat, Period: &defaults.Period, Unit: types.StandardUnit(unit)}})
	}
	return queries, nil
}

type cloudwatchReader interface {
	GetMetricData(stdctx.Context, *cw.GetMetricDataInput, ...func(*cw.Options)) (*cw.GetMetricDataOutput, error)
}

func readCloudwatch(ctx stdctx.Context, client cloudwatchReader, queries []types.MetricDataQuery, start, end time.Time, emit func(metrics.Sample) error) error {
	for offset := 0; offset < len(queries); offset += 500 {
		batch := queries[offset:min(offset+500, len(queries))]
		byID := map[string]types.MetricDataQuery{}
		for _, q := range batch {
			byID[aws.ToString(q.Id)] = q
		}
		input := &cw.GetMetricDataInput{StartTime: &start, EndTime: &end, ScanBy: types.ScanByTimestampAscending, MetricDataQueries: batch}
		seen := map[string]bool{}
		complete := map[string]bool{}
		for {
			out, err := client.GetMetricData(ctx, input)
			if err != nil {
				return err
			}
			if len(out.Messages) > 0 {
				return fmt.Errorf("CloudWatch query messages: %v", out.Messages)
			}
			for _, r := range out.MetricDataResults {
				if r.StatusCode == types.StatusCodeComplete {
					complete[aws.ToString(r.Id)] = true
				}
				if r.StatusCode != types.StatusCodeComplete && !(r.StatusCode == types.StatusCodePartialData && aws.ToString(out.NextToken) != "") {
					return fmt.Errorf("CloudWatch query %s returned %s: %v", aws.ToString(r.Id), r.StatusCode, r.Messages)
				}
				if len(r.Timestamps) != len(r.Values) {
					return fmt.Errorf("CloudWatch timestamps/value count mismatch")
				}
				q, ok := byID[aws.ToString(r.Id)]
				if !ok {
					return fmt.Errorf("unknown CloudWatch query id %s", aws.ToString(r.Id))
				}
				s := metrics.Sample{Labels: map[string]string{}}
				if q.Expression != nil {
					s.Labels["series"] = aws.ToString(r.Label)
					s.Metric = aws.ToString(q.Expression)
					if len(out.MetricDataResults) >= 500 {
						return fmt.Errorf("Metrics Insights returned its series ceiling; completeness cannot be established")
					}
				}
				if q.MetricStat != nil {
					s.Metric = aws.ToString(q.MetricStat.Metric.MetricName)
					s.Namespace = aws.ToString(q.MetricStat.Metric.Namespace)
					s.Statistic = aws.ToString(q.MetricStat.Stat)
					s.Unit = string(q.MetricStat.Unit)
					for _, d := range q.MetricStat.Metric.Dimensions {
						s.Labels[aws.ToString(d.Name)] = aws.ToString(d.Value)
					}
				}
				for i, t := range r.Timestamps {
					if t.Before(start) || !t.Before(end) {
						continue
					}
					s.Time = t
					s.Value = r.Values[i]
					if err := emit(s); err != nil {
						return err
					}
				}
			}
			token := aws.ToString(out.NextToken)
			if token == "" {
				for id := range byID {
					if !complete[id] {
						return fmt.Errorf("CloudWatch omitted completion for query %s", id)
					}
				}
				break
			}
			if seen[token] {
				return fmt.Errorf("CloudWatch repeated continuation token")
			}
			seen[token] = true
			input.NextToken = out.NextToken
		}
	}
	return nil
}
