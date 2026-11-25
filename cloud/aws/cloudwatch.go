package aws

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

const cloudWatchAPIVersion = "2010-08-01"

// CloudWatch is the CloudWatch metrics client (query protocol).
type CloudWatch struct{ svc *service }

// CloudWatch returns the CloudWatch metrics client.
func (s *Session) CloudWatch() *CloudWatch {
	return &CloudWatch{svc: s.newService("monitoring", "monitoring", "monitoring")}
}

// Dimension is one name/value pair identifying a metric series.
type Dimension struct {
	Name  string `xml:"Name"`
	Value string `xml:"Value"`
}

// Metric identifies a metric.
type Metric struct {
	Namespace  string      `xml:"Namespace"`
	MetricName string      `xml:"MetricName"`
	Dimensions []Dimension `xml:"Dimensions>member"`
}

// MetricStat selects a statistic of a metric over a period in seconds.
type MetricStat struct {
	Metric Metric
	Period int32
	Stat   string
	// Unit filters the datapoints when set.
	Unit string
}

// MetricDataQuery is one GetMetricData query: a MetricStat or an Expression.
type MetricDataQuery struct {
	ID         string
	Expression string
	// Period applies to expressions; a MetricStat carries its own.
	Period     int32
	MetricStat *MetricStat
}

// ScanByTimestampAscending returns the oldest datapoints first.
const ScanByTimestampAscending = "TimestampAscending"

// GetMetricDataInput is one GetMetricData page request.
type GetMetricDataInput struct {
	StartTime, EndTime time.Time
	ScanBy             string
	Queries            []MetricDataQuery
	NextToken          string
}

// MetricMessage is a diagnostic CloudWatch attaches to a query or response.
type MetricMessage struct {
	Code  string `xml:"Code"`
	Value string `xml:"Value"`
}

// MetricDataResult is one query's datapoints. StatusCode is Complete,
// PartialData, InternalError or Forbidden.
type MetricDataResult struct {
	ID         string          `xml:"Id"`
	Label      string          `xml:"Label"`
	StatusCode string          `xml:"StatusCode"`
	Timestamps []time.Time     `xml:"Timestamps>member"`
	Values     []float64       `xml:"Values>member"`
	Messages   []MetricMessage `xml:"Messages>member"`
}

// GetMetricDataOutput is one GetMetricData page.
type GetMetricDataOutput struct {
	Results   []MetricDataResult `xml:"MetricDataResults>member"`
	Messages  []MetricMessage    `xml:"Messages>member"`
	NextToken string             `xml:"NextToken"`
}

// Metric data status codes.
const (
	StatusCodeComplete    = "Complete"
	StatusCodePartialData = "PartialData"
)

// GetMetricData reads one page of datapoints.
func (c *CloudWatch) GetMetricData(ctx context.Context, in GetMetricDataInput) (*GetMetricDataOutput, error) {
	params := url.Values{
		"StartTime": {in.StartTime.UTC().Format(time.RFC3339)},
		"EndTime":   {in.EndTime.UTC().Format(time.RFC3339)},
	}
	if in.ScanBy != "" {
		params.Set("ScanBy", in.ScanBy)
	}
	if in.NextToken != "" {
		params.Set("NextToken", in.NextToken)
	}
	for i, q := range in.Queries {
		q.encode(params, fmt.Sprintf("MetricDataQueries.member.%d", i+1))
	}
	var out struct {
		Result GetMetricDataOutput `xml:"GetMetricDataResult"`
	}
	if err := c.svc.queryCall(ctx, cloudWatchAPIVersion, "GetMetricData", params, &out); err != nil {
		return nil, err
	}
	return &out.Result, nil
}

func (q MetricDataQuery) encode(params url.Values, prefix string) {
	params.Set(prefix+".Id", q.ID)
	if q.Expression != "" {
		params.Set(prefix+".Expression", q.Expression)
		if q.Period > 0 {
			params.Set(prefix+".Period", fmt.Sprint(q.Period))
		}
	}
	if q.MetricStat == nil {
		return
	}
	stat := prefix + ".MetricStat"
	q.MetricStat.Metric.encode(params, stat+".Metric")
	params.Set(stat+".Period", fmt.Sprint(q.MetricStat.Period))
	params.Set(stat+".Stat", q.MetricStat.Stat)
	if q.MetricStat.Unit != "" {
		params.Set(stat+".Unit", q.MetricStat.Unit)
	}
}

func (m Metric) encode(params url.Values, prefix string) {
	params.Set(prefix+".Namespace", m.Namespace)
	params.Set(prefix+".MetricName", m.MetricName)
	encodeDimensions(params, prefix+".Dimensions", m.Dimensions)
}

func encodeDimensions(params url.Values, prefix string, dimensions []Dimension) {
	for i, d := range dimensions {
		member := fmt.Sprintf("%s.member.%d", prefix, i+1)
		params.Set(member+".Name", d.Name)
		if d.Value != "" {
			params.Set(member+".Value", d.Value)
		}
	}
}

// ListMetricsInput filters a ListMetrics page. Dimension filters match on name,
// and on value when it is set.
type ListMetricsInput struct {
	Namespace  string
	MetricName string
	Dimensions []Dimension
	NextToken  string
}

// ListMetricsOutput is one ListMetrics page.
type ListMetricsOutput struct {
	Metrics   []Metric `xml:"Metrics>member"`
	NextToken string   `xml:"NextToken"`
}

// ListMetrics lists one page of metrics.
func (c *CloudWatch) ListMetrics(ctx context.Context, in ListMetricsInput) (*ListMetricsOutput, error) {
	params := url.Values{}
	for key, value := range map[string]string{"Namespace": in.Namespace, "MetricName": in.MetricName, "NextToken": in.NextToken} {
		if value != "" {
			params.Set(key, value)
		}
	}
	encodeDimensions(params, "Dimensions", in.Dimensions)
	var out struct {
		Result ListMetricsOutput `xml:"ListMetricsResult"`
	}
	if err := c.svc.queryCall(ctx, cloudWatchAPIVersion, "ListMetrics", params, &out); err != nil {
		return nil, err
	}
	return &out.Result, nil
}

// StandardUnits are the units CloudWatch accepts for a MetricStat.
var StandardUnits = []string{
	"Seconds", "Microseconds", "Milliseconds", "Bytes", "Kilobytes", "Megabytes", "Gigabytes", "Terabytes",
	"Bits", "Kilobits", "Megabits", "Gigabits", "Terabits", "Percent", "Count",
	"Bytes/Second", "Kilobytes/Second", "Megabytes/Second", "Gigabytes/Second", "Terabytes/Second",
	"Bits/Second", "Kilobits/Second", "Megabits/Second", "Gigabits/Second", "Terabits/Second",
	"Count/Second", "None",
}
