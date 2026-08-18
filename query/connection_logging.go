package query

import (
	"fmt"
	"iter"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/observability"
	"github.com/flanksource/commons/har"
	"github.com/flanksource/commons/logger"
)

type connectionOperation struct {
	ctx         context.Context
	policy      observability.Policy
	diagnostics *ProviderDiagnostics
	collector   *har.Collector
	connection  string
	provider    string
	started     time.Time
	finishOnce  sync.Once
}

func prepareConnectionOperation(ctx context.Context, req ProviderRequest) (context.Context, ProviderRequest, *connectionOperation, error) {
	connection, err := loggingConnection(ctx, req)
	if err != nil {
		return ctx, req, nil, err
	}
	policy, err := observability.PolicyFor(connection)
	if err != nil {
		return ctx, req, nil, err
	}
	if req.Diagnostics == nil {
		req.Diagnostics = NewWalkDiagnostics(req.Provider)
	}
	req.Diagnostics.RecordRendered(req.Query, req.Options)
	req.Diagnostics.RecordConnection(req.Connection)

	operation := &connectionOperation{
		ctx: ctx, policy: policy, diagnostics: req.Diagnostics,
		connection: loggingConnectionName(connection, req.Diagnostics),
		provider:   req.Provider, started: time.Now(),
	}
	if level, enabled := requestHARLevel(ctx, policy); enabled {
		cfg := ctx.HARConfig(req.Provider)
		cfg.MaxEntries = observability.DefaultCollectorEntries
		if level < logger.Trace {
			cfg.CaptureContentTypes = nil
		}
		var forward func(*har.Entry)
		if parent := ctx.HARCollector(); parent != nil {
			forward = parent.Handler()
		}
		operation.collector = har.NewCollectorWithHandler(cfg, forward)
		operation.ctx = ctx.WithHARCollector(operation.collector).WithRequestHARLevel(level)
	}
	return operation.ctx, req, operation, nil
}

func loggingConnection(ctx context.Context, req ProviderRequest) (*models.Connection, error) {
	isReference := context.IsValidConnectionURL(req.Connection)
	if isReference && ctx.CanResolveConnectionReferences() {
		connection, err := context.FindConnectionByURL(ctx, req.Connection)
		if err != nil {
			return nil, fmt.Errorf("resolve logging policy for %q: %w", req.Connection, err)
		}
		if connection == nil {
			return nil, fmt.Errorf("resolve logging policy for %q: connection not found", req.Connection)
		}
		return connection, nil
	}
	connectionType := providerConnectionType(req.Provider)
	if parsed, err := url.Parse(req.Connection); !isReference && err == nil && parsed.Scheme != "" {
		if inferred := providerConnectionType(parsed.Scheme); inferred != "" {
			connectionType = inferred
		}
	}
	return &models.Connection{Type: connectionType}, nil
}

func providerConnectionType(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "sql", "postgres", "postgresql":
		return models.ConnectionTypePostgres
	case "mysql":
		return models.ConnectionTypeMySQL
	case "sqlserver", "mssql":
		return models.ConnectionTypeSQLServer
	case "clickhouse":
		return models.ConnectionTypeClickHouse
	case "sqlite", "sqlite3":
		return models.ConnectionTypeSQLite
	case "opensearch":
		return models.ConnectionTypeOpenSearch
	case "opentelemetry":
		return models.ConnectionTypeOpenTelemetry
	case "prometheus":
		return models.ConnectionTypePrometheus
	case "loki":
		return models.ConnectionTypeLoki
	case "jaeger":
		return models.ConnectionTypeJaeger
	case "kubernetes", "k8s", "k8slogs":
		return models.ConnectionTypeKubernetes
	case "http", "https", "postgrest":
		return models.ConnectionTypeHTTP
	default:
		return provider
	}
}

func loggingConnectionName(connection *models.Connection, diagnostics *ProviderDiagnostics) string {
	if connection != nil && connection.Name != "" {
		return connection.Name
	}
	if snapshot := diagnostics.Snapshot(); snapshot != nil && snapshot.Request.Connection != "" {
		return snapshot.Request.Connection
	}
	return "inline"
}

func requestHARLevel(ctx context.Context, policy observability.Policy) (logger.LogLevel, bool) {
	if policy.Family != observability.ProviderHTTP || ctx.Logger == nil {
		return logger.Info, false
	}
	level := logger.Debug
	enabled := eventCanLog(ctx.Logger, policy, observability.EventHTTP) ||
		eventCanLog(ctx.Logger, policy, observability.EventHTTPHeaders)
	if eventCanLog(ctx.Logger, policy, observability.EventHTTPRequestBody) ||
		eventCanLog(ctx.Logger, policy, observability.EventHTTPResponseBody) {
		level = logger.Trace
		enabled = true
	}
	return level, enabled
}

func eventCanLog(log logger.Logger, policy observability.Policy, event observability.Event) bool {
	return policy.Enabled(event) && log.IsLevelEnabled(policy.Level(event))
}

func (operation *connectionOperation) Finish(rows int, runErr error) {
	if operation == nil {
		return
	}
	operation.finishOnce.Do(func() {
		if runErr != nil {
			operation.diagnostics.RecordError(runErr)
		}
		duration := time.Since(operation.started)
		event, level := operation.policy.Completion(duration, runErr)
		if operation.policy.Enabled(event) {
			operation.log(level, event, operation.summary(event, duration, rows, runErr), operation.prettySummary(event, duration, rows, runErr))
		}
		operation.logDetails(duration, rows)
	})
}

func (operation *connectionOperation) summary(event observability.Event, duration time.Duration, rows int, runErr error) map[string]any {
	values := operation.baseValues(duration, rows)
	snapshot := operation.diagnostics.Snapshot()
	if snapshot != nil {
		if snapshot.Request.URL != "" {
			values["method"] = snapshot.Request.Method
			values["url"] = snapshot.Request.URL
			values["status"] = snapshot.Response.Status
		} else if snapshot.Request.Query != "" {
			if operation.policy.Family == observability.ProviderSQL {
				values["sql"] = logger.StripSecrets(snapshot.Request.Query)
			} else {
				values["query"] = logger.StripSecrets(snapshot.Request.Query)
			}
		}
		if len(snapshot.Request.Details) > 0 {
			values["filters"] = snapshot.Request.Details
		}
	}
	if runErr != nil {
		values["error"] = logger.StripSecrets(runErr.Error())
	}
	if event == observability.EventSlow {
		values["slow_threshold_ms"] = operation.policy.SlowThreshold.Milliseconds()
	}
	if operation.collector != nil && operation.collector.DroppedEntries() > 0 {
		values["dropped_entries"] = operation.collector.DroppedEntries()
	}
	return values
}

func (operation *connectionOperation) baseValues(duration time.Duration, rows int) map[string]any {
	return map[string]any{
		"provider": operation.provider, "connection": operation.connection,
		"duration_ms": duration.Milliseconds(), "rows": rows,
	}
}

func (operation *connectionOperation) logDetails(duration time.Duration, rows int) {
	snapshot := operation.diagnostics.Snapshot()
	if snapshot != nil && len(snapshot.Request.Arguments) > 0 && eventCanLog(operation.ctx.Logger, operation.policy, observability.EventSQLParams) {
		params := redactArguments(snapshot.Request.Arguments)
		operation.log(
			operation.policy.Level(observability.EventSQLParams),
			observability.EventSQLParams,
			mergeLogValues(operation.baseValues(duration, rows), map[string]any{"params": params}),
			operation.prettyParams(params),
		)
	}
	if operation.collector == nil {
		return
	}
	for _, entry := range operation.collector.Entries() {
		operation.logHTTPEntry(entry, rows)
	}
}

func (operation *connectionOperation) logHTTPEntry(entry har.Entry, rows int) {
	base := mergeLogValues(operation.baseValues(time.Duration(entry.Time*float64(time.Millisecond)), rows), map[string]any{
		"method": entry.Request.Method, "url": entry.Request.URL, "status": entry.Response.Status,
	})
	if eventCanLog(operation.ctx.Logger, operation.policy, observability.EventHTTPHeaders) {
		operation.log(
			operation.policy.Level(observability.EventHTTPHeaders),
			observability.EventHTTPHeaders,
			mergeLogValues(base, map[string]any{
				"request_headers": entry.Request.Headers, "query": entry.Request.QueryString, "response_headers": entry.Response.Headers,
			}),
			operation.prettyHTTPEntry(entry, har.DetailOptions{RequestHeaders: true, QueryString: true, ResponseHeaders: true}),
		)
	}
	if entry.Request.PostData != nil && entry.Request.PostData.Text != "" && eventCanLog(operation.ctx.Logger, operation.policy, observability.EventHTTPRequestBody) {
		operation.log(
			operation.policy.Level(observability.EventHTTPRequestBody),
			observability.EventHTTPRequestBody,
			mergeLogValues(base, map[string]any{"request_body": entry.Request.PostData.Text, "body_size": entry.Request.BodySize}),
			operation.prettyHTTPEntry(entry, har.DetailOptions{RequestBody: true}),
		)
	}
	if entry.Response.Content.Text != "" && eventCanLog(operation.ctx.Logger, operation.policy, observability.EventHTTPResponseBody) {
		operation.log(
			operation.policy.Level(observability.EventHTTPResponseBody),
			observability.EventHTTPResponseBody,
			mergeLogValues(base, map[string]any{
				"response_body": entry.Response.Content.Text, "body_size": entry.Response.BodySize, "truncated": entry.Response.Content.Truncated,
			}),
			operation.prettyHTTPEntry(entry, har.DetailOptions{ResponseBody: true}),
		)
	}
}

func redactArguments(arguments []any) []any {
	redacted := make([]any, len(arguments))
	for index, argument := range arguments {
		if value, ok := argument.(string); ok {
			redacted[index] = logger.PrintableSecret(value)
		} else {
			redacted[index] = argument
		}
	}
	return redacted
}

func mergeLogValues(base, extra map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

func withConnectionLogging(operation *connectionOperation, pages iter.Seq2[Page, error]) iter.Seq2[Page, error] {
	return func(yield func(Page, error) bool) {
		rows := 0
		var runErr error
		defer func() { operation.Finish(rows, runErr) }()
		for page, err := range pages {
			if err != nil {
				runErr = err
				yield(Page{}, err)
				return
			}
			rows += len(page.Rows)
			if !yield(page, nil) {
				return
			}
		}
	}
}
