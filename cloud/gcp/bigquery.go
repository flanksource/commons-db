package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
)

const (
	bigQueryDefaultRoot = "https://bigquery.googleapis.com"
	bigQueryAPIPath     = "/bigquery/v2"

	// bigQueryWait is how long the server holds a query or poll request open
	// waiting for the job to finish before answering jobComplete=false.
	bigQueryWait = 10 * time.Second
)

// BigQueryClient talks to the BigQuery REST API.
type BigQueryClient struct{ *api }

// NewBigQueryClient builds a BigQuery client. An Endpoint may carry the
// /bigquery/v2 path, as emulator endpoints usually do.
func NewBigQueryClient(opts Options) (*BigQueryClient, error) {
	client, err := newAPI(opts, bigQueryDefaultRoot, bigQueryAPIPath)
	if err != nil {
		return nil, err
	}
	return &BigQueryClient{api: client}, nil
}

// QueryRequest is a standard-SQL query to run.
type QueryRequest struct {
	Project string
	SQL     string
	// Location pins the job's location; empty lets BigQuery choose.
	Location string
}

// QueryResult is a finished query: all result rows, decoded per the schema.
type QueryResult struct {
	Schema BigQuerySchema
	// Rows hold decoded values: string, int64, float64, bool, time.Time ([]byte
	// for BYTES, *big.Rat for NUMERIC, []any for REPEATED and RECORD, nil for NULL).
	Rows      [][]any
	TotalRows int64
	Stats     *JobStatistics
}

// JobStatistics are the execution statistics of a finished query job.
type JobStatistics struct {
	CreationTime   time.Time
	StartTime      time.Time
	EndTime        time.Time
	BytesProcessed int64
	BytesBilled    int64
	SlotMillis     int64
}

type jobReference struct {
	ProjectID string `json:"projectId"`
	JobID     string `json:"jobId"`
	Location  string `json:"location"`
}

type queryResponse struct {
	JobComplete  bool         `json:"jobComplete"`
	JobReference jobReference `json:"jobReference"`
	Schema       struct {
		Fields []*BigQueryField `json:"fields"`
	} `json:"schema"`
	Rows      []bigQueryRow `json:"rows"`
	TotalRows string        `json:"totalRows"`
	PageToken string        `json:"pageToken"`
}

type bigQueryRow struct {
	F []struct {
		V any `json:"v"`
	} `json:"f"`
}

// Query runs the SQL, waits for the job to finish and reads every result page.
func (b *BigQueryClient) Query(ctx context.Context, req QueryRequest) (*QueryResult, error) {
	if req.Project == "" || req.SQL == "" {
		return nil, fmt.Errorf("gcp: bigquery query needs a project and SQL")
	}
	first, err := b.startQuery(ctx, req)
	if err != nil {
		return nil, err
	}
	result := &QueryResult{Schema: first.Schema.Fields}
	ref := first.JobReference
	if ref.JobID == "" {
		return nil, fmt.Errorf("gcp: bigquery response has no job reference")
	}
	for page := first; ; {
		if err := result.addPage(page); err != nil {
			return nil, err
		}
		if page.PageToken == "" {
			break
		}
		if page, err = b.queryResults(ctx, ref, page.PageToken); err != nil {
			return nil, err
		}
	}
	if result.TotalRows, err = strconv.ParseInt(first.TotalRows, 10, 64); err != nil {
		return nil, fmt.Errorf("gcp: bigquery totalRows %q: %w", first.TotalRows, err)
	}
	if result.Stats, err = b.jobStatistics(ctx, ref); err != nil {
		return nil, err
	}
	return result, nil
}

// startQuery submits the query and polls until the job completes.
func (b *BigQueryClient) startQuery(ctx context.Context, req QueryRequest) (*queryResponse, error) {
	body := map[string]any{
		"query":           req.SQL,
		"useLegacySql":    false,
		"timeoutMs":       bigQueryWait.Milliseconds(),
		"jobCreationMode": "JOB_CREATION_REQUIRED",
		// requestId makes the POST idempotent: the transport retries 5xx with this
		// same body, and BigQuery dedupes the job on the id instead of billing twice.
		"requestId": uuid.NewString(),
	}
	if req.Location != "" {
		body["location"] = req.Location
	}
	resp := &queryResponse{}
	err := b.do(ctx, call{Method: http.MethodPost, URL: b.projectURL(req.Project) + "/queries", Body: body, Out: resp})
	if err != nil {
		return nil, fmt.Errorf("gcp: starting bigquery query: %w", err)
	}
	for !resp.JobComplete {
		if resp, err = b.queryResults(ctx, resp.JobReference, ""); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

// queryResults is jobs.getQueryResults: it waits up to bigQueryWait for the job.
func (b *BigQueryClient) queryResults(ctx context.Context, ref jobReference, pageToken string) (*queryResponse, error) {
	if ref.JobID == "" {
		return nil, fmt.Errorf("gcp: bigquery query is incomplete but the response has no job reference")
	}
	query := url.Values{"timeoutMs": {strconv.FormatInt(bigQueryWait.Milliseconds(), 10)}}
	if ref.Location != "" {
		query.Set("location", ref.Location)
	}
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}
	var resp queryResponse
	target := withQuery(b.projectURL(ref.ProjectID)+"/queries/"+url.PathEscape(ref.JobID), query)
	if err := b.do(ctx, call{Method: http.MethodGet, URL: target, Out: &resp}); err != nil {
		return nil, fmt.Errorf("gcp: reading bigquery results of job %s: %w", ref.JobID, err)
	}
	if resp.JobReference.JobID == "" {
		resp.JobReference = ref
	}
	return &resp, nil
}

func (b *BigQueryClient) projectURL(project string) string {
	return b.base + "/projects/" + url.PathEscape(project)
}

func (r *QueryResult) addPage(page *queryResponse) error {
	for _, row := range page.Rows {
		decoded, err := decodeRow(r.Schema, row)
		if err != nil {
			return err
		}
		r.Rows = append(r.Rows, decoded)
	}
	return nil
}

type jobStatisticsWire struct {
	Statistics struct {
		CreationTime json.Number `json:"creationTime"`
		StartTime    json.Number `json:"startTime"`
		EndTime      json.Number `json:"endTime"`
		Query        struct {
			TotalBytesProcessed json.Number `json:"totalBytesProcessed"`
			TotalBytesBilled    json.Number `json:"totalBytesBilled"`
			TotalSlotMs         json.Number `json:"totalSlotMs"`
		} `json:"query"`
	} `json:"statistics"`
}

// jobStatistics reads the statistics of a finished job (jobs.get).
func (b *BigQueryClient) jobStatistics(ctx context.Context, ref jobReference) (*JobStatistics, error) {
	query := url.Values{}
	if ref.Location != "" {
		query.Set("location", ref.Location)
	}
	var wire jobStatisticsWire
	target := withQuery(b.projectURL(ref.ProjectID)+"/jobs/"+url.PathEscape(ref.JobID), query)
	if err := b.do(ctx, call{Method: http.MethodGet, URL: target, Out: &wire}); err != nil {
		return nil, fmt.Errorf("gcp: reading bigquery job %s statistics: %w", ref.JobID, err)
	}
	s := wire.Statistics
	stats := &JobStatistics{}
	var err error
	for _, field := range []struct {
		raw json.Number
		set func(int64)
	}{
		{s.CreationTime, func(v int64) { stats.CreationTime = time.UnixMilli(v).UTC() }},
		{s.StartTime, func(v int64) { stats.StartTime = time.UnixMilli(v).UTC() }},
		{s.EndTime, func(v int64) { stats.EndTime = time.UnixMilli(v).UTC() }},
		{s.Query.TotalBytesProcessed, func(v int64) { stats.BytesProcessed = v }},
		{s.Query.TotalBytesBilled, func(v int64) { stats.BytesBilled = v }},
		{s.Query.TotalSlotMs, func(v int64) { stats.SlotMillis = v }},
	} {
		if field.raw == "" {
			continue
		}
		var value int64
		if value, err = field.raw.Int64(); err != nil {
			return nil, fmt.Errorf("gcp: bigquery job %s statistic %q: %w", ref.JobID, field.raw, err)
		}
		field.set(value)
	}
	return stats, nil
}
