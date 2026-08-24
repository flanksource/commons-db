package providers

import (
	"fmt"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs"
	"github.com/flanksource/commons-db/logs/azureloganalytics"
	"github.com/flanksource/commons-db/query"
)

func init() {
	query.RegisterProvider(&azureLogsProvider{})
}

// azureLogsProvider runs a KQL query against an Azure Monitor Log Analytics
// workspace and returns one row per result row.
type azureLogsProvider struct{}

func (azureLogsProvider) Type() string { return "azureloganalytics" }

type azureLogsOptions struct {
	// WorkspaceID is the Log Analytics workspace the query runs against.
	WorkspaceID string `json:"workspaceID,omitempty"`

	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
	Limit string `json:"limit,omitempty"`

	Mapping *logs.FieldMappingConfig `json:"mapping,omitempty"`
}

func (azureLogsProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	opts, err := query.DecodeOptions[azureLogsOptions](req.Options)
	if err != nil {
		return nil, err
	}

	if opts.WorkspaceID == "" {
		return nil, fmt.Errorf("azureloganalytics requires the `workspaceID` option")
	}

	request := azureloganalytics.Request{
		WorkspaceID: opts.WorkspaceID,
		Query:       req.Query,
	}
	request.Start = opts.Start
	request.End = opts.End
	request.Limit = opts.Limit

	conn := connection.AzureConnection{ConnectionName: req.Connection}
	result, err := azureloganalytics.New(conn, opts.Mapping).Search(ctx, request)
	if err != nil {
		return nil, err
	}

	return logResultToRows(result), nil
}
