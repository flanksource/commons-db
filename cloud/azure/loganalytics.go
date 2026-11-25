package azure

import (
	"context"
	"errors"
	"net/url"
	"strings"

	commonshttp "github.com/flanksource/commons/http"
	"github.com/flanksource/commons/http/middlewares/azuread"
)

const (
	// DefaultLogAnalyticsEndpoint is the public-cloud Log Analytics API root.
	DefaultLogAnalyticsEndpoint = "https://api.loganalytics.io/v1"
	// DefaultLogAnalyticsScope is the public-cloud Log Analytics token scope.
	DefaultLogAnalyticsScope = "https://api.loganalytics.io/.default"
)

// LogAnalyticsOptions configures a LogAnalyticsClient. Endpoint and Scope
// replace the Azure SDK's cloud configuration for sovereign clouds, e.g.
// Endpoint "https://api.loganalytics.us/v1" with Scope
// "https://api.loganalytics.us/.default". The token authority is configured on
// Credential (azuread.ClientSecret.Authority).
type LogAnalyticsOptions struct {
	Credential azuread.Credential
	// Endpoint is the API root including the version, default DefaultLogAnalyticsEndpoint.
	Endpoint string
	// Scope is the token scope, default DefaultLogAnalyticsScope.
	Scope string
	// SkipTLSVerify disables server certificate verification.
	SkipTLSVerify bool
}

// LogAnalyticsClient queries Log Analytics workspaces.
type LogAnalyticsClient struct {
	client   *commonshttp.Client
	endpoint string
}

// QueryRequest is one workspace query. Timespan is an ISO8601 "start/end"
// interval and is omitted when empty.
type QueryRequest struct {
	WorkspaceID string
	Query       string
	Timespan    string
}

// QueryResult holds the tables returned by a query. Error is set when the
// service returned a partial failure alongside a 2xx response.
type QueryResult struct {
	Tables []Table      `json:"tables"`
	Error  *ErrorDetail `json:"error,omitempty"`
}

// Table is one result table. Row values are JSON-decoded (numbers are float64,
// nulls are nil).
type Table struct {
	Name    string   `json:"name"`
	Columns []Column `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// Column is a result table column.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type queryBody struct {
	Query    string `json:"query"`
	Timespan string `json:"timespan,omitempty"`
}

// NewLogAnalyticsClient builds a Log Analytics client.
func NewLogAnalyticsClient(opts LogAnalyticsOptions) (*LogAnalyticsClient, error) {
	endpoint, scope := opts.Endpoint, opts.Scope
	if endpoint == "" {
		endpoint = DefaultLogAnalyticsEndpoint
	}
	if scope == "" {
		scope = DefaultLogAnalyticsScope
	}
	client, err := newClient(opts.Credential, scope, opts.SkipTLSVerify)
	if err != nil {
		return nil, err
	}
	return &LogAnalyticsClient{client: client, endpoint: strings.TrimRight(endpoint, "/")}, nil
}

// Query runs a KQL query against a workspace.
func (c *LogAnalyticsClient) Query(ctx context.Context, req QueryRequest) (*QueryResult, error) {
	if req.WorkspaceID == "" {
		return nil, errors.New("azure: workspaceID is required")
	}
	if req.Query == "" {
		return nil, errors.New("azure: query is required")
	}
	endpoint := c.endpoint + "/workspaces/" + url.PathEscape(req.WorkspaceID) + "/query"
	var out QueryResult
	if err := postJSON(ctx, c.client, endpoint, nil, queryBody{Query: req.Query, Timespan: req.Timespan}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
