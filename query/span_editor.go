package query

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/otel/span"
	"github.com/flanksource/commons-db/otel/span/layout"
)

type SpanFieldMapping struct {
	Source   string `json:"source,omitempty" yaml:"source,omitempty"`
	CEL      string `json:"cel,omitempty" yaml:"cel,omitempty"`
	JSONPath string `json:"jsonpath,omitempty" yaml:"jsonpath,omitempty"`
	Unit     string `json:"unit,omitempty" yaml:"unit,omitempty"`
}
type SpanMappings map[string]SpanFieldMapping

func (m SpanMappings) ValidateReversible() error {
	if err := m.Validate(); err != nil {
		return err
	}
	for name, field := range m {
		if field.CEL != "" || field.JSONPath != "" || name == "attributes" || name == "resource" {
			return fmt.Errorf("canonical search cannot invert span mapping %s; configure individual fields in connection span_layout", name)
		}
	}
	return nil
}

func ValidateSpanOptions(options map[string]any) error {
	for _, key := range []string{"traceIdField", "spanIdField", "parentIdField", "parentRefType", "serviceField", "operationField", "statusFields"} {
		if _, exists := options[key]; exists {
			return fmt.Errorf("provider.options.%s was removed; configure canonical fields in spans", key)
		}
	}
	return nil
}

func DefaultSpanColumns() []ColumnDef {
	return []ColumnDef{
		{Name: "timestamp", Label: "Timestamp", Type: ColumnTypeDateTime, Kind: ColumnKindTimestamp},
		{Name: "service", Label: "Service", Type: ColumnTypeString},
		{Name: "operation", Label: "Operation", Type: ColumnTypeString},
		{Name: "duration_ms", Label: "Duration", Type: ColumnTypeDuration, Unit: "ms"},
		{Name: "status", Label: "Status", Type: ColumnTypeString, Kind: ColumnKindStatus},
		{Name: "trace_id", Label: "Trace ID", Type: ColumnTypeString, Hidden: true},
		{Name: "span_id", Label: "Span ID", Type: ColumnTypeString, Hidden: true},
		{Name: "parent_id", Label: "Parent ID", Type: ColumnTypeString, Hidden: true},
	}
}

func SpanColumns(rows []Row) []ColumnDef {
	columns := append(DefaultSpanColumns(), ColumnDef{Name: "kind", Label: "Kind", Type: ColumnTypeString})
	for _, group := range []struct{ name, source string }{{"attributes", "_span_attributes"}, {"resource", "_resource"}} {
		values := map[string]Row{}
		for _, row := range rows {
			if attrs, ok := row[group.source].(map[string]any); ok {
				for key, value := range attrs {
					values[key] = Row{"value": value}
				}
			}
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			quoted, _ := json.Marshal(key)
			column := InferSampleColumns([]Row{values[key]})[0]
			column.Name, column.Label, column.Source, column.JSONPath = group.name+"."+key, group.name+" · "+key, group.source, "$["+string(quoted)+"]"
			columns = append(columns, column)
		}
	}
	return columns
}

func (m SpanMappings) Validate() error {
	for name, field := range m {
		if !span.IsCanonicalField(name) {
			return fmt.Errorf("unknown canonical span field %q", name)
		}
		column := ColumnDef{Name: name, Source: field.Source, CEL: field.CEL, JSONPath: field.JSONPath}
		if err := column.Validate(); err != nil {
			return err
		}
		if field.Source == "" && field.CEL == "" && field.JSONPath == "" {
			return fmt.Errorf("span %s requires source, cel or jsonpath", name)
		}
		if field.Unit != "" {
			if name != "startedAt" && name != "durationMs" {
				return fmt.Errorf("span %s does not accept a unit", name)
			}
			if !span.ValidMappingUnit(field.Unit) {
				return fmt.Errorf("span %s: unsupported unit %q", name, field.Unit)
			}
		}
	}
	return nil
}

func spanMappingValue(ctx context.Context, field SpanFieldMapping, row Row) (any, error) {
	var value any
	var err error
	switch {
	case field.CEL != "":
		value, err = evalRowCEL(ctx, field.CEL, row)
	case field.JSONPath != "":
		expression, compileErr := compileColumnJSONPath(ColumnDef{JSONPath: field.JSONPath})
		if compileErr != nil {
			return nil, compileErr
		}
		value, err = evalRowJSONPath(expression, field.Source, row)
	default:
		value = row[field.Source]
		if value == nil {
			expression, compileErr := compileColumnJSONPath(ColumnDef{JSONPath: "$." + field.Source})
			if compileErr != nil {
				return nil, compileErr
			}
			value, err = evalRowJSONPath(expression, "", row)
		}
	}
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("source produced no value")
	}
	return value, nil
}

func mappedSpanDocument(ctx context.Context, profile Profile, row Row) (span.Span, error) {
	document := map[string]any{}
	for key, value := range row {
		document[key] = value
	}
	if sourceLayout, ok := row["_span_layout"]; ok && row["_trace"] == nil {
		encoded, err := json.Marshal(sourceLayout)
		if err != nil {
			return span.Span{}, err
		}
		var resolved layout.Definition
		if err := json.Unmarshal(encoded, &resolved); err != nil {
			return span.Span{}, err
		}
		canonical, err := resolved.Normalize(map[string]any(row))
		if err != nil {
			return span.Span{}, err
		}
		document = canonical.Row()
	}
	if retained, ok := row["_trace"]; ok {
		switch value := retained.(type) {
		case span.Span:
			document = value.Row()
		case map[string]any:
			document = make(map[string]any, len(value))
			for name, field := range value {
				document[name] = field
			}
		default:
			return span.Span{}, fmt.Errorf("canonical span must be an object, received %T", retained)
		}
	}
	values := map[string]any{}
	units := map[string]string{}
	for name, field := range profile.Spans {
		value, err := spanMappingValue(ctx, field, row)
		if err != nil {
			return span.Span{}, fmt.Errorf("span %s: %w", name, err)
		}
		values[name], units[name] = value, field.Unit
	}
	return span.NormalizeMapped(document, values, units)
}
