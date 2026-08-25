package providers

import (
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs"
	"github.com/flanksource/commons-db/logs/gcpcloudlogging"
	"github.com/flanksource/commons-db/query"
)

func init() {
	query.RegisterProvider(&gcpLoggingProvider{})
}

// gcpLoggingProvider runs a Cloud Logging filter expression and returns one row
// per log entry.
type gcpLoggingProvider struct{}

func (gcpLoggingProvider) Type() string { return "gcpcloudlogging" }

type gcpLoggingOptions struct {
	// Project overrides the project carried by the connection.
	Project string `json:"project,omitempty"`

	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
	Limit string `json:"limit,omitempty"`

	Mapping *logs.FieldMappingConfig `json:"mapping,omitempty"`
}

func (gcpLoggingProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	opts, err := query.DecodeOptions[gcpLoggingOptions](req.Options)
	if err != nil {
		return nil, err
	}

	conn, err := gcpConnectionForRequest(ctx, req.Connection, opts.Project)
	if err != nil {
		return nil, err
	}

	searcher, err := gcpcloudlogging.New(ctx, conn, opts.Mapping)
	if err != nil {
		return nil, err
	}
	defer func() { _ = searcher.Close() }()

	request := gcpcloudlogging.Request{Filter: req.Query}
	request.Start = opts.Start
	request.End = opts.End
	request.Limit = opts.Limit

	result, err := searcher.Search(ctx, request)
	if err != nil {
		return nil, err
	}

	return logResultToRows(result), nil
}
