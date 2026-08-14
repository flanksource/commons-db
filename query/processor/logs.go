package processor

import (
	"fmt"
	"maps"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs"
	"github.com/flanksource/commons-db/query"
)

const defaultLogColumn = "message"

// LogParseConfig selects the structured format and source column used by the
// logs.parse processor.
type LogParseConfig struct {
	// Format is json, logfmt, klogfmt, syslog or autodetect. Empty means
	// autodetect.
	Format string `json:"format,omitempty" yaml:"format,omitempty"`

	// Column carries the raw log body. Empty reads message.
	Column string `json:"column,omitempty" yaml:"column,omitempty"`
}

func (c LogParseConfig) Validate() error {
	return logs.ValidateFormat(c.Format)
}

func (c LogParseConfig) column() string {
	if c.Column == "" {
		return defaultLogColumn
	}
	return c.Column
}

// ApplyLogParse turns structured bodies into canonical log columns, promotes
// their remaining fields without replacing provider metadata, and recomputes
// hash from the parsed message for downstream logs.dedupe.
func ApplyLogParse(rows []query.Row, config LogParseConfig) ([]query.Row, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	out := make([]query.Row, len(rows))
	for index, row := range rows {
		value, ok := row[config.column()]
		if !ok {
			return nil, fmt.Errorf("row %d has no %q column", index, config.column())
		}
		message, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("row %d column %q is %T, want string", index, config.column(), value)
		}

		parsed := logs.LogLine{Message: message}
		if value, ok := row["severity"].(string); ok {
			parsed.Severity = value
		}
		if value, ok := row["source"].(string); ok {
			parsed.Source = value
		}
		if value, ok := row["host"].(string); ok {
			parsed.Host = value
		}
		logs.ParseMessage(&parsed, config.Format)
		parsed.SetHash()

		next := query.Row(maps.Clone(row))
		next["message"] = parsed.Message
		next["hash"] = parsed.Hash
		setParsedLogFields(next, parsed)
		out[index] = next
	}
	return out, nil
}

func setParsedLogFields(row query.Row, parsed logs.LogLine) {
	for key, value := range map[string]string{
		"severity": parsed.Severity,
		"source":   parsed.Source,
		"host":     parsed.Host,
	} {
		if value != "" {
			row[key] = value
		}
	}
	for key, value := range parsed.Labels {
		if _, exists := row[key]; !exists {
			row[key] = value
		}
	}
}

func init() {
	query.RegisterProcessor(logParseProcessor{})
}

type logParseProcessor struct{}

func (logParseProcessor) Type() string { return "logs.parse" }

func (logParseProcessor) Process(_ context.Context, spec query.ProcessorSpec, in *query.Result) (*query.Result, error) {
	config, err := query.DecodeOptions[LogParseConfig](spec.Config)
	if err != nil {
		return nil, err
	}
	rows, err := ApplyLogParse(in.Rows, config)
	if err != nil {
		return nil, err
	}
	result := *in
	result.Rows = rows
	return &result, nil
}

func (logParseProcessor) ProcessPage(_ context.Context, spec query.ProcessorSpec, page query.Page) (query.Page, error) {
	config, err := query.DecodeOptions[LogParseConfig](spec.Config)
	if err != nil {
		return query.Page{}, err
	}
	rows, err := ApplyLogParse(page.Rows, config)
	if err != nil {
		return query.Page{}, err
	}
	page.Rows = rows
	return page, nil
}
