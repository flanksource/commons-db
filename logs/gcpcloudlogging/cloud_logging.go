package gcpcloudlogging

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/commons-db/cloud/gcp"
	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs"
	"github.com/flanksource/commons/collections"
)

// Request represents available parameters for GCP Cloud Logging queries.
//
// +kubebuilder:object:generate=true
type Request struct {
	logs.LogsRequestBase `json:",inline" template:"true"`

	// Filter is the filter to perform
	Filter string `json:"filter,omitempty" template:"true"`
}

type cloudLogging struct {
	client        *gcp.LoggingClient
	project       string
	mappingConfig *logs.FieldMappingConfig
}

// Close is a no-op: the REST client holds no resources that outlive a request.
func (c *cloudLogging) Close() error {
	return nil
}

func New(ctx context.Context, conn connection.GCPConnection, mappingConfig *logs.FieldMappingConfig) (*cloudLogging, error) {
	if conn.Project == "" {
		return nil, fmt.Errorf("a GCP project is required to read Cloud Logging entries")
	}

	// The connection arrives hydrated — the provider is the single hydration
	// point, so nothing here re-reads the row or re-resolves its secret.
	var credentials string
	if conn.Credentials != nil && !conn.Credentials.IsEmpty() {
		credentials = conn.Credentials.ValueStatic
	}
	opts, err := conn.ClientOptions(ctx, credentials, gcp.ScopeLoggingRead)
	if err != nil {
		return nil, err
	}
	// Absent credentials are not an error: the client falls through to the
	// ambient application-default chain.
	if opts.TokenSource == nil {
		if opts.TokenSource, err = gcp.DefaultTokenSource(ctx, gcp.ScopeLoggingRead); err != nil {
			return nil, err
		}
	}

	client, err := gcp.NewLoggingClient(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create logging client: %w", err)
	}

	return &cloudLogging{
		client:        client,
		project:       conn.Project,
		mappingConfig: mappingConfig,
	}, nil
}

func (c *cloudLogging) Search(ctx context.Context, request Request) (*logs.LogResult, error) {
	var maxLogLines = 1000
	if request.Limit != "" {
		if l, err := strconv.ParseInt(request.Limit, 10, 32); err == nil && l > 0 {
			maxLogLines = int(l)
		}
	}

	mappingConfig := defaultFieldMappingConfig
	if c.mappingConfig != nil {
		mappingConfig = c.mappingConfig.WithDefaults(defaultFieldMappingConfig)
	}

	result := &logs.LogResult{
		Logs: make([]*logs.LogLine, 0),
	}

	combinedFilter, err := buildFilter(request)
	if err != nil {
		return nil, err
	}

	listOptions := gcp.ListEntriesOptions{
		ResourceNames: []string{"projects/" + c.project},
		Filter:        combinedFilter,
		OrderBy:       "timestamp desc",
		PageSize:      min(maxLogLines, 1000),
	}

	err = c.client.EachEntry(ctx, listOptions, func(entry *gcp.LogEntry) (bool, error) {
		if entry.Payload() == nil {
			return false, nil
		}

		line, err := entryToLogLine(ctx, entry, mappingConfig)
		if err != nil {
			return false, err
		}

		result.Logs = append(result.Logs, line)
		return len(result.Logs) >= maxLogLines, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list access log entries: %w", err)
	}

	return result, nil
}

func buildFilter(request Request) (string, error) {
	filterParts := []string{}
	if request.Filter != "" {
		filterParts = append(filterParts, request.Filter)
	}

	if request.Start != "" {
		startTime, err := request.GetStart()
		if err != nil {
			return "", fmt.Errorf("invalid start time: %w", err)
		}
		filterParts = append(filterParts, fmt.Sprintf(`timestamp >= "%s"`, startTime.Format("2006-01-02T15:04:05Z")))
	}

	if request.End != "" {
		endTime, err := request.GetEnd()
		if err != nil {
			return "", fmt.Errorf("invalid end time: %w", err)
		}
		filterParts = append(filterParts, fmt.Sprintf(`timestamp <= "%s"`, endTime.Format("2006-01-02T15:04:05Z")))
	}

	var combinedFilter string
	if len(filterParts) > 0 {
		wrappedParts := make([]string, len(filterParts))
		for i, part := range filterParts {
			wrappedParts[i] = fmt.Sprintf("(%s)", part)
		}
		combinedFilter = strings.Join(wrappedParts, " AND ")
	}
	return defaultTimestampFilter(combinedFilter, time.Now()), nil
}

// defaultTimestampFilter reproduces logadmin's implicit window: a filter that
// never mentions "timestamp" (any case, even inside a payload match) is
// restricted to the last 24 hours.
func defaultTimestampFilter(filter string, now time.Time) string {
	dayAgo := now.Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	switch {
	case len(filter) == 0:
		return fmt.Sprintf(`timestamp >= "%s"`, dayAgo)
	case !strings.Contains(strings.ToLower(filter), "timestamp"):
		return fmt.Sprintf(`%s AND timestamp >= "%s"`, filter, dayAgo)
	default:
		return filter
	}
}

func entryToLogLine(ctx context.Context, entry *gcp.LogEntry, mappingConfig logs.FieldMappingConfig) (*logs.LogLine, error) {
	line := &logs.LogLine{
		Count: 1,
	}

	if entry.InsertID != "" {
		line.ID = entry.InsertID
	}
	if !entry.Timestamp.IsZero() {
		line.FirstObserved = entry.Timestamp
	}
	if severity := entry.SeverityName(); severity != "" {
		line.Severity = severity
	}
	if entry.LogName != "" {
		line.Source = entry.LogName
	}

	switch payload := entry.Payload().(type) {
	case string:
		line.Message = payload
	default:
		payloadMap, err := collections.ToJSONMap(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to convert payload to JSON map: %w", err)
		}

		for k, v := range payloadMap {
			if err := logs.MapFieldToLogLine(k, v, line, mappingConfig); err != nil {
				ctx.Warnf("Error mapping field %s for log %s: %v", k, line.ID, err)
			}
		}
	}

	if entry.Resource != nil && entry.Resource.Labels != nil {
		for k, v := range entry.Resource.Labels {
			resourceKey := "resource." + k
			if err := logs.MapFieldToLogLine(resourceKey, v, line, mappingConfig); err != nil {
				ctx.Warnf("Error mapping resource field %s for log %s: %v", resourceKey, line.ID, err)
			}
		}
	}

	if entry.Labels != nil {
		for k, v := range entry.Labels {
			labelKey := "label." + k
			if err := logs.MapFieldToLogLine(labelKey, v, line, mappingConfig); err != nil {
				ctx.Warnf("Error mapping label field %s for log %s: %v", labelKey, line.ID, err)
			}
		}
	}

	line.SetHash()
	return line, nil
}

// defaultFieldMappingConfig can vary widely depending on the source of the log.
//
//	Example: Audit logs have very different fields than a kubernetes pod log.
var defaultFieldMappingConfig = logs.FieldMappingConfig{
	Message:   []string{"message", "msg", "textPayload"},
	Timestamp: []string{"timestamp", "@timestamp", "lastTimestamp"},
	Severity:  []string{"severity", "level"},
	Source:    []string{"logName", "source"},
	Host:      []string{"resource.instance_id", "resource.pod_name", "resource.container_name"},
}
