package providers

import (
	"fmt"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
)

// gcpConnectionForRequest hydrates a GCP connection for a provider request.
//
// project overrides whatever the connection carries. It is required one way or
// the other — every GCP client is scoped to a project, and an empty one reaches
// the SDK as a request for the project named "", which fails far from here.
func gcpConnectionForRequest(ctx context.Context, connectionName, project string) (connection.GCPConnection, error) {
	conn := connection.GCPConnection{ConnectionName: connectionName, Project: project}
	if err := conn.HydrateConnection(ctx); err != nil {
		return conn, fmt.Errorf("hydrate gcp connection: %w", err)
	}

	if conn.Project == "" {
		return conn, fmt.Errorf("gcp project is required: set the provider option `project`, or a `project` property on connection %q", connectionName)
	}

	return conn, nil
}
