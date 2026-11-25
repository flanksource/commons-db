package aws

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// The operations in this file provision fixtures — buckets, keys, log events,
// metric datapoints — in emulators and integration tests. No commons-db caller
// reads through them in production.

// CreateBucket creates a bucket in the session region.
func (c *S3) CreateBucket(ctx context.Context, bucket string) error {
	base, path := c.target(bucket, "")
	op := call{Operation: "CreateBucket", Method: http.MethodPut, BaseURL: base, Path: path}
	if region := c.session.Region(); region != "us-east-1" {
		op.Body = `<CreateBucketConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><LocationConstraint>` + region + `</LocationConstraint></CreateBucketConfiguration>`
	}
	return c.svc.do(ctx, op, nil, nil)
}

// CreateKey creates a symmetric encryption key and returns its id.
func (c *KMS) CreateKey(ctx context.Context) (string, error) {
	var out struct {
		KeyMetadata struct {
			KeyID string `json:"KeyId"`
		} `json:"KeyMetadata"`
	}
	if err := c.svc.jsonRPC(ctx, kmsProtocol, "CreateKey", struct{}{}, &out); err != nil {
		return "", err
	}
	return out.KeyMetadata.KeyID, nil
}

// CreateLogGroup creates a log group.
func (c *Logs) CreateLogGroup(ctx context.Context, group string) error {
	in := struct {
		LogGroupName string `json:"logGroupName"`
	}{group}
	return c.svc.jsonRPC(ctx, logsProtocol, "CreateLogGroup", in, nil)
}

// CreateLogStream creates a log stream in a group.
func (c *Logs) CreateLogStream(ctx context.Context, group, stream string) error {
	in := struct {
		LogGroupName  string `json:"logGroupName"`
		LogStreamName string `json:"logStreamName"`
	}{group, stream}
	return c.svc.jsonRPC(ctx, logsProtocol, "CreateLogStream", in, nil)
}

// LogEvent is one event for PutLogEvents; Timestamp is epoch milliseconds.
type LogEvent struct {
	Timestamp int64  `json:"timestamp"`
	Message   string `json:"message"`
}

// PutLogEvents appends events to a log stream.
func (c *Logs) PutLogEvents(ctx context.Context, group, stream string, events []LogEvent) error {
	in := struct {
		LogGroupName  string     `json:"logGroupName"`
		LogStreamName string     `json:"logStreamName"`
		LogEvents     []LogEvent `json:"logEvents"`
	}{group, stream, events}
	return c.svc.jsonRPC(ctx, logsProtocol, "PutLogEvents", in, nil)
}

// MetricDatum is one datapoint for PutMetricData.
type MetricDatum struct {
	MetricName string
	Dimensions []Dimension
	Timestamp  time.Time
	Value      float64
	Unit       string
}

// PutMetricData publishes datapoints under a namespace.
func (c *CloudWatch) PutMetricData(ctx context.Context, namespace string, data []MetricDatum) error {
	params := url.Values{"Namespace": {namespace}}
	for i, d := range data {
		prefix := fmt.Sprintf("MetricData.member.%d", i+1)
		params.Set(prefix+".MetricName", d.MetricName)
		params.Set(prefix+".Value", fmt.Sprint(d.Value))
		params.Set(prefix+".Timestamp", d.Timestamp.UTC().Format(time.RFC3339))
		if d.Unit != "" {
			params.Set(prefix+".Unit", d.Unit)
		}
		encodeDimensions(params, prefix+".Dimensions", d.Dimensions)
	}
	return c.svc.queryCall(ctx, cloudWatchAPIVersion, "PutMetricData", params, nil)
}
