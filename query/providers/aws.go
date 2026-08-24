package providers

import (
	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
)

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
		resolved, err := resolveInlineURL(ctx, endpoint, models.ConnectionTypeAWS)
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
