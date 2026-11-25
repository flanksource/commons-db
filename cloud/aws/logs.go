package aws

import "context"

var logsProtocol = jsonProtocol{Version: "1.1", TargetPrefix: "Logs_20140328"}

// Logs is the CloudWatch Logs client (JSON 1.1).
type Logs struct{ svc *service }

// Logs returns the CloudWatch Logs client.
func (s *Session) Logs() *Logs { return &Logs{svc: s.newService("logs", "logs", "logs")} }

// QueryStatus is the state of a Logs Insights query.
type QueryStatus string

// Logs Insights query states.
const (
	QueryStatusScheduled QueryStatus = "Scheduled"
	QueryStatusRunning   QueryStatus = "Running"
	QueryStatusComplete  QueryStatus = "Complete"
	QueryStatusFailed    QueryStatus = "Failed"
	QueryStatusCancelled QueryStatus = "Cancelled"
	QueryStatusTimeout   QueryStatus = "Timeout"
)

// StartQueryInput starts a Logs Insights query. StartTime and EndTime are epoch
// seconds, as the API defines them.
type StartQueryInput struct {
	LogGroupName string `json:"logGroupName"`
	QueryString  string `json:"queryString"`
	StartTime    int64  `json:"startTime"`
	EndTime      int64  `json:"endTime"`
	// Limit caps the returned rows when positive.
	Limit int32 `json:"limit,omitempty"`
}

// QueryResultField is one column of one result row.
type QueryResultField struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// QueryStatistics are the scan statistics of a query.
type QueryStatistics struct {
	RecordsMatched float64 `json:"recordsMatched"`
	RecordsScanned float64 `json:"recordsScanned"`
	BytesScanned   float64 `json:"bytesScanned"`
}

// QueryResults is the GetQueryResults response.
type QueryResults struct {
	Status     QueryStatus          `json:"status"`
	Results    [][]QueryResultField `json:"results"`
	Statistics QueryStatistics      `json:"statistics"`
}

// StartQuery starts a query and returns its id.
func (c *Logs) StartQuery(ctx context.Context, in StartQueryInput) (string, error) {
	var out struct {
		QueryID string `json:"queryId"`
	}
	if err := c.svc.jsonRPC(ctx, logsProtocol, "StartQuery", in, &out); err != nil {
		return "", err
	}
	return out.QueryID, nil
}

// GetQueryResults returns the current status and results of a query.
func (c *Logs) GetQueryResults(ctx context.Context, queryID string) (*QueryResults, error) {
	var out QueryResults
	in := struct {
		QueryID string `json:"queryId"`
	}{queryID}
	if err := c.svc.jsonRPC(ctx, logsProtocol, "GetQueryResults", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
