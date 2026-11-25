package bigquery

import (
	"fmt"
	"sync"

	"github.com/flanksource/commons-db/cloud/gcp"
	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs"
)

// Searcher implements log searching using BigQuery.
type Searcher struct {
	conn          connection.GCPConnection
	mappingConfig *logs.FieldMappingConfig
	bqClient      *gcp.BigQueryClient
	clientMutex   sync.Mutex
}

// New creates a new BigQuery log searcher.
func New(conn connection.GCPConnection, mappingConfig *logs.FieldMappingConfig) *Searcher {
	return &Searcher{
		conn:          conn,
		mappingConfig: mappingConfig,
	}
}

// Search executes a log search query using BigQuery.
func (s *Searcher) Search(ctx context.Context, request Request) (*logs.LogResult, error) {
	if err := s.initializeClients(ctx); err != nil {
		return nil, fmt.Errorf("failed to initialize clients: %w", err)
	}

	sqlQuery := request.Query
	if sqlQuery == "" {
		return nil, fmt.Errorf("query is required")
	}

	response, err := s.executeQuery(ctx, sqlQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to execute query: %w", err)
	}

	mappingConfig := logs.FieldMappingConfig{}
	if s.mappingConfig != nil {
		mappingConfig = *s.mappingConfig
	}

	result, err := response.ToLogResult(mappingConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to convert response to log result: %w", err)
	}

	return &result, nil
}

// initializeClients creates and caches BigQuery client.
func (s *Searcher) initializeClients(ctx context.Context) error {
	s.clientMutex.Lock()
	defer s.clientMutex.Unlock()

	if s.bqClient != nil {
		return nil
	}

	if s.conn.Project == "" {
		return fmt.Errorf("a GCP project is required to query BigQuery")
	}

	// The connection arrives hydrated — the provider is the single hydration
	// point, so nothing here re-reads the row or re-resolves its secret.
	//
	// The connection's credentials have to be handed to the client explicitly;
	// without this the hydrated service account is resolved and then ignored,
	// and the client quietly authenticates as whatever ambient default
	// credentials the process happens to have. A connection that carries none is
	// asking for exactly that ambient chain.
	var credentials string
	if s.conn.Credentials != nil && !s.conn.Credentials.IsEmpty() {
		credentials = s.conn.Credentials.ValueStatic
	}
	opts, err := s.conn.ClientOptions(ctx, credentials, gcp.ScopeBigQuery)
	if err != nil {
		return err
	}
	if opts.TokenSource == nil {
		if opts.TokenSource, err = gcp.DefaultTokenSource(ctx, gcp.ScopeBigQuery); err != nil {
			return err
		}
	}

	client, err := gcp.NewBigQueryClient(opts)
	if err != nil {
		return fmt.Errorf("failed to create BigQuery client: %w", err)
	}
	s.bqClient = client

	return nil
}

// executeQuery runs the BigQuery SQL query and returns results.
func (s *Searcher) executeQuery(ctx context.Context, sqlQuery string) (*Response, error) {
	s.clientMutex.Lock()
	client := s.bqClient
	s.clientMutex.Unlock()

	result, err := client.Query(ctx, gcp.QueryRequest{Project: s.conn.Project, SQL: sqlQuery})
	if err != nil {
		return nil, err
	}

	rows := make([]Row, len(result.Rows))
	for i, row := range result.Rows {
		rows[i] = row
	}

	response := &Response{
		Rows:      rows,
		Schema:    result.Schema,
		TotalRows: result.TotalRows,
	}
	if result.Stats != nil {
		response.JobStats = &JobStats{
			CreationTime:   result.Stats.CreationTime,
			StartTime:      result.Stats.StartTime,
			EndTime:        result.Stats.EndTime,
			BytesProcessed: result.Stats.BytesProcessed,
			BytesBilled:    result.Stats.BytesBilled,
			TotalSlotMs:    result.Stats.SlotMillis,
		}
	}
	return response, nil
}

// Close releases the cached client; the REST client holds no connections of its own.
func (s *Searcher) Close() error {
	s.clientMutex.Lock()
	defer s.clientMutex.Unlock()

	s.bqClient = nil
	return nil
}
