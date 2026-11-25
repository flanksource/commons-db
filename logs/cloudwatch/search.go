package cloudwatch

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/samber/lo"

	cloudaws "github.com/flanksource/commons-db/cloud/aws"
	"github.com/flanksource/commons-db/logs"
)

// defaultPollInterval is how long Search waits between GetQueryResults calls
// while a query is scheduled or running.
const defaultPollInterval = time.Second

type Searcher struct {
	client        *cloudaws.Logs
	mappingConfig *logs.FieldMappingConfig

	// PollInterval overrides the wait between GetQueryResults polls; zero means one second.
	PollInterval time.Duration
}

func New(client *cloudaws.Logs, mappingConfig *logs.FieldMappingConfig) *Searcher {
	return &Searcher{
		client:        client,
		mappingConfig: mappingConfig,
	}
}

func (t *Searcher) Search(ctx context.Context, request Request) (*logs.LogResult, error) {
	input, err := queryInput(request)
	if err != nil {
		return nil, err
	}

	queryID, err := t.client.StartQuery(ctx, input)
	if err != nil {
		return nil, err
	}

	queryResult, err := t.getQueryResults(ctx, queryID)
	if err != nil {
		return nil, err
	}

	logResult := logs.LogResult{
		Metadata: map[string]any{
			"total":      int(queryResult.Statistics.RecordsMatched),
			"statistics": queryResult.Statistics,
			"query":      request.Query,
			"logGroup":   request.LogGroup,
		},
		Logs: make([]*logs.LogLine, 0, len(queryResult.Results)),
	}

	mappingConfig := DefaultFieldMappingConfig()
	if t.mappingConfig != nil {
		mappingConfig = t.mappingConfig.WithDefaults(DefaultFieldMappingConfig())
	}

	for _, fields := range queryResult.Results {
		line := &logs.LogLine{
			Count: 1,
		}

		for _, field := range fields {
			if err := logs.MapFieldToLogLine(field.Field, field.Value, line, mappingConfig); err != nil {
				return nil, fmt.Errorf("error mapping field %s: %w", field.Field, err)
			}
		}

		line.SetHash()
		logResult.Logs = append(logResult.Logs, line)
	}

	return &logResult, nil
}

// queryInput maps a request onto StartQuery. The API takes epoch seconds and
// requires an end time, which defaults to now.
func queryInput(request Request) (cloudaws.StartQueryInput, error) {
	input := cloudaws.StartQueryInput{LogGroupName: request.LogGroup, QueryString: request.Query}

	if request.Limit != "" {
		limit, err := strconv.ParseInt(request.Limit, 10, 32)
		if err != nil {
			return input, err
		}
		input.Limit = int32(limit)
	}

	if s, err := request.GetStart(); err == nil {
		input.StartTime = s.Unix()
	}

	if e, err := request.GetEnd(); err == nil {
		input.EndTime = e.Unix()
	} else {
		input.EndTime = time.Now().Unix()
	}
	return input, nil
}

func (t *Searcher) getQueryResults(ctx context.Context, queryID string) (*cloudaws.QueryResults, error) {
	interval := lo.Ternary(t.PollInterval > 0, t.PollInterval, defaultPollInterval)

	for {
		resp, err := t.client.GetQueryResults(ctx, queryID)
		if err != nil {
			return nil, fmt.Errorf("failed to get query results for query %s: %w", queryID, err)
		}

		switch resp.Status {
		case cloudaws.QueryStatusComplete:
			return resp, nil
		case cloudaws.QueryStatusFailed:
			return nil, fmt.Errorf("cloudwatch query %s failed", queryID)
		case cloudaws.QueryStatusTimeout:
			return nil, fmt.Errorf("cloudwatch query %s timed out", queryID)
		case cloudaws.QueryStatusCancelled:
			return nil, fmt.Errorf("cloudwatch query %s was cancelled", queryID)
		}

		// Scheduled, running or an unrecognised status: still processing.
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("query cancelled while waiting: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
}

func DefaultFieldMappingConfig() logs.FieldMappingConfig {
	return logs.FieldMappingConfig{
		ID:        []string{"@ptr"},
		Ignore:    []string{"@log", ""},
		Source:    []string{"@logStream"},
		Message:   []string{"@message"},
		Timestamp: []string{"@timestamp"},
	}
}
