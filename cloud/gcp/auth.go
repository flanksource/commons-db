package gcp

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Scopes the clients in this package request.
const (
	ScopeCloudPlatform  = "https://www.googleapis.com/auth/cloud-platform"
	ScopeDevStorageFull = "https://www.googleapis.com/auth/devstorage.full_control"
	ScopeBigQuery       = "https://www.googleapis.com/auth/bigquery"
	ScopeLoggingRead    = "https://www.googleapis.com/auth/logging.read"
	ScopeKMS            = "https://www.googleapis.com/auth/cloudkms"
	ScopePubSub         = "https://www.googleapis.com/auth/pubsub"
	ScopeSQLAdmin       = "https://www.googleapis.com/auth/sqlservice.admin"
	ScopeSQLLogin       = "https://www.googleapis.com/auth/sqlservice.login"
)

// CredentialsTokenSource returns a token source for a service-account (or
// authorized-user) JSON credential. The source outlives ctx's cancellation:
// token refreshes happen long after the call that created it returned.
func CredentialsTokenSource(ctx context.Context, credentialsJSON []byte, scopes ...string) (oauth2.TokenSource, error) {
	creds, err := google.CredentialsFromJSON(context.WithoutCancel(ctx), credentialsJSON, scopes...)
	if err != nil {
		return nil, fmt.Errorf("gcp: loading credentials: %w", err)
	}
	return creds.TokenSource, nil
}

// DefaultTokenSource returns a token source from the ambient application
// default credentials (GOOGLE_APPLICATION_CREDENTIALS, gcloud, metadata server).
func DefaultTokenSource(ctx context.Context, scopes ...string) (oauth2.TokenSource, error) {
	creds, err := google.FindDefaultCredentials(context.WithoutCancel(ctx), scopes...)
	if err != nil {
		return nil, fmt.Errorf("gcp: finding application default credentials: %w", err)
	}
	return creds.TokenSource, nil
}
