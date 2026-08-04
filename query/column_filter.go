package query

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/flanksource/commons-db/context"
)

const columnFilterPrefix = "filter."

var (
	directCELField  = regexp.MustCompile(`^(?:row|span)\.([A-Za-z_][A-Za-z0-9_.@-]*)$`)
	indexedCELField = regexp.MustCompile(`^(?:row|span)\[['"]([^'"]+)['"]\]$`)
)

type ColumnFilterDef struct {
	Field string `json:"field" yaml:"field"`
}

type ColumnFilterBinding struct {
	Column string
	Key    string
	Field  string
	Label  string
}

type ColumnFilterValue struct {
	Column  string
	Key     string
	Field   string
	Include []string
	Exclude []string
}

func (p Profile) ColumnFilterBindings() ([]ColumnFilterBinding, error) {
	if p.Provider.Type != "opensearch" && p.Provider.Type != "opentelemetry" {
		return nil, nil
	}
	bindings := make([]ColumnFilterBinding, 0, len(p.Columns))
	for _, column := range p.Columns {
		if column.Hidden || !columnFilterScalar(column.Type) {
			continue
		}
		field, ok, err := columnFilterField(column)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if column.Filter == nil {
			field = openTelemetryFilterField(p, field)
		}
		label := column.Label
		if label == "" {
			label = column.Name
		}
		bindings = append(bindings, ColumnFilterBinding{
			Column: column.Name,
			Key:    columnFilterPrefix + column.Name,
			Field:  field,
			Label:  label,
		})
	}
	return bindings, nil
}

func (p Profile) ColumnFilterKeys() (map[string]string, error) {
	bindings, err := p.ColumnFilterBindings()
	if err != nil {
		return nil, err
	}
	keys := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		keys[binding.Column] = binding.Key
	}
	return keys, nil
}

func columnFilterScalar(columnType ColumnType) bool {
	switch columnType {
	case ColumnTypeKeyValue, ColumnTypeKeyValues, ColumnTypeJSON:
		return false
	default:
		return true
	}
}

func columnFilterField(column ColumnDef) (string, bool, error) {
	if column.Filter != nil {
		field := strings.TrimSpace(column.Filter.Field)
		if field == "" {
			return "", false, fmt.Errorf("column %q filter field is required", column.Name)
		}
		return field, true, nil
	}
	if column.Source != "" {
		return column.Source, true, nil
	}
	expression := strings.TrimSpace(column.CEL)
	if expression == "" {
		return column.Name, column.Name != "", nil
	}
	if matches := directCELField.FindStringSubmatch(expression); len(matches) == 2 {
		return matches[1], true, nil
	}
	if matches := indexedCELField.FindStringSubmatch(expression); len(matches) == 2 {
		return matches[1], true, nil
	}
	return "", false, nil
}

func openTelemetryFilterField(profile Profile, inferred string) string {
	if profile.Provider.Type != "opentelemetry" {
		return inferred
	}
	defaults := map[string]string{
		"timestamp":      "@timestamp",
		"trace_id":       "trace_id",
		"span_id":        "span_id",
		"parent_id":      "parent_id",
		"service":        "service_name",
		"service_name":   "service_name",
		"operation":      "operation_name",
		"operation_name": "operation_name",
	}
	optionNames := map[string]string{
		"timestamp":      "dateField",
		"trace_id":       "traceIdField",
		"span_id":        "spanIdField",
		"parent_id":      "parentIdField",
		"service":        "serviceField",
		"service_name":   "serviceField",
		"operation":      "operationField",
		"operation_name": "operationField",
	}
	optionName, known := optionNames[inferred]
	if !known {
		return inferred
	}
	if configured, ok := profile.Provider.Options[optionName].(string); ok && strings.TrimSpace(configured) != "" {
		return configured
	}
	return defaults[inferred]
}

func partitionProfileInput(profile Profile, input map[string]any) (map[string]any, []ColumnFilterValue, error) {
	bindings, err := profile.ColumnFilterBindings()
	if err != nil {
		return nil, nil, err
	}
	byKey := make(map[string]ColumnFilterBinding, len(bindings))
	for _, binding := range bindings {
		byKey[binding.Key] = binding
	}
	params := make(map[string]any, len(input))
	values := make(map[string]ColumnFilterValue, len(bindings))
	for key, value := range input {
		if !strings.HasPrefix(key, columnFilterPrefix) {
			params[key] = value
			continue
		}
		binding, ok := byKey[key]
		if !ok {
			return nil, nil, fmt.Errorf("column filter %q is not supported by profile %q", key, profile.Name)
		}
		include, exclude, err := parseColumnFilterSelection(value)
		if err != nil {
			return nil, nil, fmt.Errorf("column filter %q: %w", key, err)
		}
		if len(include) == 0 && len(exclude) == 0 {
			continue
		}
		values[key] = ColumnFilterValue{
			Column: binding.Column, Key: binding.Key, Field: binding.Field,
			Include: include, Exclude: exclude,
		}
	}
	filters := make([]ColumnFilterValue, 0, len(values))
	for _, binding := range bindings {
		if value, ok := values[binding.Key]; ok {
			filters = append(filters, value)
		}
	}
	return params, filters, nil
}

func LookupFilterValues(ctx context.Context, profile Profile, input map[string]any, key, search string, limit int) ([]FilterOption, int, error) {
	if err := profile.Validate(); err != nil {
		return nil, 0, err
	}
	if profile.Namespace != "" {
		ctx = ctx.WithNamespace(profile.Namespace)
	}
	profileParams, filters, err := partitionProfileInput(profile, input)
	if err != nil {
		return nil, 0, fmt.Errorf("profile %q: %w", profile.Name, err)
	}
	bindings, err := profile.ColumnFilterBindings()
	if err != nil {
		return nil, 0, fmt.Errorf("profile %q: %w", profile.Name, err)
	}
	var binding *ColumnFilterBinding
	for i := range bindings {
		if bindings[i].Key == key {
			binding = &bindings[i]
			break
		}
	}
	if binding == nil {
		return nil, 0, fmt.Errorf("column filter %q is not supported by profile %q", key, profile.Name)
	}
	siblings := filters[:0]
	for _, filter := range filters {
		if filter.Key != key {
			siblings = append(siblings, filter)
		}
	}
	resolved, err := resolveParams(profile.Params, profileParams)
	if err != nil {
		return nil, 0, fmt.Errorf("profile %q: %w", profile.Name, err)
	}
	provider, err := GetProvider(profile.Provider.Type)
	if err != nil {
		return nil, 0, err
	}
	lookup, ok := provider.(FilterLookupProvider)
	if !ok {
		return nil, 0, fmt.Errorf("provider %q does not support column filter lookups", profile.Provider.Type)
	}
	req, err := buildProviderRequest(ctx, profile.Provider, profile.Query, profile.Params, resolved)
	if err != nil {
		return nil, 0, fmt.Errorf("profile %q: %w", profile.Name, err)
	}
	req.Filters = siblings
	return lookup.LookupFilterValues(ctx, req, *binding, search, limit)
}

func parseColumnFilterSelection(value any) ([]string, []string, error) {
	var raw []string
	switch typed := value.(type) {
	case string:
		raw = []string{typed}
	case []string:
		raw = typed
	case []any:
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, nil, fmt.Errorf("value must be a string, got %T", item)
			}
			raw = append(raw, text)
		}
	default:
		return nil, nil, fmt.Errorf("value must be a string or string list, got %T", value)
	}
	include, exclude := []string{}, []string{}
	seenInclude, seenExclude := map[string]bool{}, map[string]bool{}
	for _, joined := range raw {
		for _, item := range strings.Split(joined, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if strings.HasPrefix(item, "!") {
				item = strings.TrimSpace(strings.TrimPrefix(item, "!"))
				if item == "" {
					return nil, nil, fmt.Errorf("excluded value must not be empty")
				}
				if !seenExclude[item] {
					exclude = append(exclude, item)
					seenExclude[item] = true
				}
			} else if !seenInclude[item] {
				include = append(include, item)
				seenInclude[item] = true
			}
		}
	}
	return include, exclude, nil
}
