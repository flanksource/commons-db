package observability

import (
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons/logger"
)

const (
	PropertyErrorLevel            = "log.level.error"
	PropertySlowLevel             = "log.level.slow"
	PropertySlowThreshold         = "log.slowThreshold"
	PropertyOperationLevel        = "log.level.operation"
	PropertySQLLevel              = "log.level.sql"
	PropertySQLParamsLevel        = "log.level.sql.params"
	PropertyHTTPLevel             = "log.level.http"
	PropertyHTTPHeadersLevel      = "log.level.http.headers"
	PropertyHTTPRequestBodyLevel  = "log.level.http.request.body"
	PropertyHTTPResponseBodyLevel = "log.level.http.response.body"
	DefaultCollectorEntries       = 100
)

type Event string

const (
	EventError            Event = "error"
	EventSlow             Event = "slow"
	EventOperation        Event = "operation"
	EventSQL              Event = "sql"
	EventSQLParams        Event = "sql_params"
	EventHTTP             Event = "http"
	EventHTTPHeaders      Event = "http_headers"
	EventHTTPRequestBody  Event = "http_request_body"
	EventHTTPResponseBody Event = "http_response_body"
)

type ProviderFamily string

const (
	ProviderGeneric ProviderFamily = "generic"
	ProviderSQL     ProviderFamily = "sql"
	ProviderHTTP    ProviderFamily = "http"
)

type EventDefinition struct {
	Event         Event          `json:"event"`
	Property      string         `json:"property"`
	Label         string         `json:"label"`
	Description   string         `json:"description"`
	Default       string         `json:"default"`
	Captures      []string       `json:"captures"`
	Example       map[string]any `json:"example"`
	PrettyExample string         `json:"prettyExample"`
}

type Capability struct {
	Family         ProviderFamily    `json:"family"`
	SlowThreshold  string            `json:"slowThreshold"`
	ThresholdLabel string            `json:"thresholdLabel"`
	Events         []EventDefinition `json:"events"`
}

type eventLevel struct {
	level   logger.LogLevel
	enabled bool
}

type Policy struct {
	Family        ProviderFamily
	SlowThreshold time.Duration
	levels        map[Event]eventLevel
}

var supportedLevels = map[string]logger.LogLevel{
	"error":  logger.Error,
	"warn":   logger.Warn,
	"info":   logger.Info,
	"debug":  logger.Debug,
	"trace":  logger.Trace,
	"trace1": logger.Trace1,
	"trace2": logger.Trace2,
	"trace3": logger.Trace3,
	"trace4": logger.Trace4,
}

var commonEvents = []EventDefinition{
	event(EventError, PropertyErrorLevel, "Errors", "Failed operations, including the sanitized request identity.", "error", []string{"error", "operation", "duration"}),
	event(EventSlow, PropertySlowLevel, "Slow operations", "Successful operations at or above the slow threshold.", "warn", []string{"operation", "duration", "slow threshold"}),
}

var providerEvents = map[ProviderFamily][]EventDefinition{
	ProviderGeneric: {
		event(EventOperation, PropertyOperationLevel, "Operation summary", "One completion record for a successful provider operation.", "debug", []string{"operation", "duration", "result summary"}),
	},
	ProviderSQL: {
		event(EventSQL, PropertySQLLevel, "SQL statement", "The sanitized SQL statement and its completion metrics.", "trace", []string{"statement", "duration", "rows"}),
		event(EventSQLParams, PropertySQLParamsLevel, "SQL parameters", "Bound arguments, with credential-shaped values redacted.", "trace1", []string{"bound arguments"}),
	},
	ProviderHTTP: {
		event(EventHTTP, PropertyHTTPLevel, "Access summary", "One method, sanitized URL, status and duration record per request.", "debug", []string{"method", "URL", "status", "duration"}),
		event(EventHTTPHeaders, PropertyHTTPHeadersLevel, "Headers and parameters", "Sanitized request and response headers plus query parameters.", "trace", []string{"headers", "query parameters"}),
		event(EventHTTPRequestBody, PropertyHTTPRequestBodyLevel, "Request body", "A bounded sanitized request body.", "trace1", []string{"request body"}),
		event(EventHTTPResponseBody, PropertyHTTPResponseBodyLevel, "Response body", "A bounded sanitized response body with explicit truncation metadata.", "trace2", []string{"response body"}),
	},
}

func event(kind Event, property, label, description, defaultLevel string, captures []string) EventDefinition {
	return EventDefinition{
		Event: kind, Property: property, Label: label, Description: description,
		Default: defaultLevel, Captures: captures,
	}
}

func CapabilityFor(connectionType string) Capability {
	family := familyFor(connectionType)
	events := append(cloneDefinitions(commonEvents), cloneDefinitions(providerEvents[family])...)
	previewProvider := strings.TrimSpace(connectionType)
	if previewProvider == "" {
		previewProvider = string(family)
	}
	for index := range events {
		events[index].Example = runtimeJSONExample(events[index], family, previewProvider)
		events[index].PrettyExample = runtimePrettyExample(events[index], family, previewProvider)
	}
	return Capability{
		Family:         family,
		SlowThreshold:  "1s",
		ThresholdLabel: "Slow threshold",
		Events:         events,
	}
}

func PolicyFor(connection *models.Connection) (Policy, error) {
	connectionType := ""
	properties := map[string]string(nil)
	if connection != nil {
		connectionType = connection.Type
		properties = connection.Properties
	}
	capability := CapabilityFor(connectionType)
	policy := Policy{
		Family:        capability.Family,
		SlowThreshold: time.Second,
		levels:        make(map[Event]eventLevel, len(capability.Events)),
	}
	if raw := strings.TrimSpace(properties[PropertySlowThreshold]); raw != "" {
		threshold, err := time.ParseDuration(raw)
		if err != nil {
			return Policy{}, fmt.Errorf("connection property %s: %w", PropertySlowThreshold, err)
		}
		if threshold <= 0 {
			return Policy{}, fmt.Errorf("connection property %s must be positive, got %q", PropertySlowThreshold, raw)
		}
		policy.SlowThreshold = threshold
	}
	for _, definition := range capability.Events {
		value := definition.Default
		if override := strings.TrimSpace(properties[definition.Property]); override != "" {
			value = strings.ToLower(override)
		}
		if value == "off" {
			policy.levels[definition.Event] = eventLevel{}
			continue
		}
		level, ok := supportedLevels[value]
		if !ok {
			return Policy{}, fmt.Errorf("connection property %s has unsupported log level %q", definition.Property, value)
		}
		policy.levels[definition.Event] = eventLevel{level: level, enabled: true}
	}
	return policy, nil
}

func (p Policy) Level(event Event) logger.LogLevel {
	return p.levels[event].level
}

func (p Policy) Enabled(event Event) bool {
	return p.levels[event].enabled
}

func (p Policy) Completion(duration time.Duration, err error) (Event, logger.LogLevel) {
	selected := EventOperation
	if err != nil {
		selected = EventError
	} else if duration >= p.SlowThreshold {
		selected = EventSlow
	} else {
		switch p.Family {
		case ProviderSQL:
			selected = EventSQL
		case ProviderHTTP:
			selected = EventHTTP
		}
	}
	return selected, p.Level(selected)
}

func SupportedLevelNames() []string {
	return []string{"off", "error", "warn", "info", "debug", "trace", "trace1", "trace2", "trace3", "trace4"}
}

func familyFor(connectionType string) ProviderFamily {
	switch connectionType {
	case models.ConnectionTypePostgres, models.ConnectionTypeMySQL, models.ConnectionTypeSQLServer,
		models.ConnectionTypeClickHouse, models.ConnectionTypeSQLite:
		return ProviderSQL
	case models.ConnectionTypeHTTP, models.ConnectionTypeOpenSearch, models.ConnectionTypeOpenTelemetry,
		models.ConnectionTypePrometheus, models.ConnectionTypeLoki, models.ConnectionTypeJaeger,
		models.ConnectionTypeKubernetes:
		return ProviderHTTP
	default:
		return ProviderGeneric
	}
}

func cloneDefinitions(source []EventDefinition) []EventDefinition {
	out := make([]EventDefinition, len(source))
	for i, definition := range source {
		out[i] = definition
		out[i].Captures = append([]string(nil), definition.Captures...)
		out[i].Example = cloneMap(definition.Example)
	}
	return out
}

func cloneMap(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
