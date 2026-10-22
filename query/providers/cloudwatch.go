package providers

import (
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs"
	"github.com/flanksource/commons-db/logs/cloudwatch"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers/internal/providerkit"
)

func init() {
	query.RegisterProvider(&cloudwatchProvider{})
}

// cloudwatchProvider runs a CloudWatch Logs Insights query against a log group
// and returns one row per log line.
type cloudwatchProvider struct{}

func (cloudwatchProvider) Type() string { return "cloudwatch" }

type cloudwatchOptions struct {
	// LogGroup is the log group the Insights query runs against.
	LogGroup string `json:"logGroup,omitempty"`

	// Region overrides the region carried by the connection.
	Region string `json:"region,omitempty"`

	// Endpoint points the client somewhere other than the AWS endpoint for the
	// region — a VPC endpoint, or LocalStack.
	Endpoint string `json:"endpoint,omitempty"`

	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
	Limit string `json:"limit,omitempty"`

	Mapping *logs.FieldMappingConfig `json:"mapping,omitempty"`
}

// defaultCloudWatchStart bounds an unbounded query. StartQuery requires both
// ends of the range, and the searcher only defaults the end, so without this a
// profile that omits `start` fails inside the SDK rather than here.
const defaultCloudWatchStart = "now-1h"

func (cloudwatchProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	opts, err := query.DecodeOptions[cloudwatchOptions](req.Options)
	if err != nil {
		return nil, err
	}

	if opts.LogGroup == "" {
		return nil, fmt.Errorf("cloudwatch requires the `logGroup` option")
	}

	cfg, endpoint, err := awsConfigForRequest(ctx, req.Connection, opts.Region, opts.Endpoint)
	if err != nil {
		return nil, err
	}

	client := cloudwatchlogs.NewFromConfig(cfg, func(o *cloudwatchlogs.Options) {
		if endpoint != "" {
			o.BaseEndpoint = &endpoint
		}
	})

	request := cloudwatch.Request{
		LogGroup: opts.LogGroup,
		Query:    req.Query,
	}
	request.Start = opts.Start
	if request.Start == "" {
		request.Start = defaultCloudWatchStart
	}
	request.End = opts.End
	request.Limit = opts.Limit

	result, err := cloudwatch.New(client, opts.Mapping).Search(ctx, request)
	if err != nil {
		return nil, err
	}

	return logResultToRows(result), nil
}

// awsConfigForRequest hydrates an AWS connection and returns the SDK config
// alongside the endpoint to talk to.
//
// The endpoint comes back separately because AWSConnection.Client does not
// apply it — a caller pointing at LocalStack or a VPC endpoint has to set
// BaseEndpoint on the service client itself.
func awsConfigForRequest(ctx context.Context, connectionName, region, endpoint string) (aws.Config, string, error) {
	conn := connection.AWSConnection{ConnectionName: connectionName, Region: region}
	if err := conn.Populate(ctx); err != nil {
		return aws.Config{}, "", err
	}

	if endpoint != "" {
		resolved, err := providerkit.ResolveInlineURL(ctx, endpoint, models.ConnectionTypeAWS)
		if err != nil {
			return aws.Config{}, "", err
		}
		conn.Endpoint = resolved
	}

	cfg, err := conn.Client(ctx)
	if err != nil {
		return aws.Config{}, "", err
	}
	return cfg, conn.Endpoint, nil
}
