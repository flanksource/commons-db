package providers

import (
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs"
	"github.com/flanksource/commons-db/logs/bigquery"
	"github.com/flanksource/commons-db/query"
)

func init() {
	query.RegisterProvider(&bigqueryProvider{})
}

// bigqueryProvider runs standard SQL against BigQuery and maps each result row
// to a log line.
//
// There is no time range here: bigquery.Request carries only a query, so a
// profile's time params have to appear in the SQL itself.
type bigqueryProvider struct{}

func (bigqueryProvider) Type() string { return "bigquery" }

type bigqueryOptions struct {
	// Project overrides the project carried by the connection.
	Project string `json:"project,omitempty"`

	// Mapping names the columns that carry the message, timestamp and severity.
	// Worth setting: with no mapping the searcher has no defaults to fall back
	// on and every row's message becomes the whole row, stringified.
	Mapping *logs.FieldMappingConfig `json:"mapping,omitempty"`
}

func (bigqueryProvider) Execute(ctx context.Context, req query.ProviderRequest) ([]query.Row, error) {
	opts, err := query.DecodeOptions[bigqueryOptions](req.Options)
	if err != nil {
		return nil, err
	}

	conn, err := gcpConnectionForRequest(ctx, req.Connection, opts.Project)
	if err != nil {
		return nil, err
	}

	searcher := bigquery.New(conn, opts.Mapping)
	defer func() { _ = searcher.Close() }()

	result, err := searcher.Search(ctx, bigquery.Request{Query: req.Query})
	if err != nil {
		return nil, err
	}

	return logResultToRows(result), nil
}
