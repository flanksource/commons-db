package providers

import (
	"fmt"
	"os"

	"golang.org/x/oauth2/google"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
)

// gcpProjectEnvVars are the variables gcloud and the Google client libraries
// agree name the active project, checked in the order gcloud resolves them.
var gcpProjectEnvVars = []string{"GOOGLE_CLOUD_PROJECT", "GCLOUD_PROJECT"}

// gcpConnectionForRequest hydrates a GCP connection for a provider request.
//
// project overrides whatever the connection carries. It is required one way or
// the other — every GCP client is scoped to a project, and an empty one reaches
// the SDK as a request for the project named "", which fails far from here.
//
// With no connection named the credentials come from the ambient environment
// (application default credentials, workload identity, the metadata server), so
// the project is looked for there too rather than being demanded of a caller
// that supplied neither.
func gcpConnectionForRequest(ctx context.Context, connectionName, project string) (connection.GCPConnection, error) {
	conn := connection.GCPConnection{ConnectionName: connectionName, Project: project}
	if err := conn.HydrateConnection(ctx); err != nil {
		return conn, fmt.Errorf("hydrate gcp connection: %w", err)
	}

	if conn.Project == "" {
		conn.Project = ambientGCPProject(ctx)
	}

	if conn.Project == "" {
		return conn, fmt.Errorf("gcp project is required: set the provider option `project`, a `project` property on connection %q, or GOOGLE_CLOUD_PROJECT", connectionName)
	}

	return conn, nil
}

// ambientGCPProject reads the project the environment is pointed at, returning
// "" when there is none. A missing or unreadable credential is not an error
// here — the caller reports the project it could not find, which is the thing
// the operator has to fix.
func ambientGCPProject(ctx context.Context) string {
	for _, name := range gcpProjectEnvVars {
		if project := os.Getenv(name); project != "" {
			return project
		}
	}

	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return ""
	}
	return creds.ProjectID
}
