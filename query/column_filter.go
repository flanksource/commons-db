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

// SupportsNativeFilters reports whether a provider type turns
// ProviderRequest.Filters into backend query clauses. Column-filter bindings and
// tri-state list params both gate on it, so a profile can never accept an
// exclusion the provider would quietly drop.
//
// It is a declared list rather than a registry probe on purpose: the schema and
// OpenAPI generators, and every test in the external query_test package, run
// without linking query/providers, and a probe would report false there and
// silently delete every binding.
func SupportsNativeFilters(providerType string) bool {
	switch providerType {
	case "opensearch", "opentelemetry":
		return true
	default:
		return false
	}
}

func (p Profile) ColumnFilterBindings() ([]ColumnFilterBinding, error) {
	if !SupportsNativeFilters(p.Provider.Type) {
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

// ParamFilterBindings returns the bindings the profile's tri-state list params
// contribute. They carry no Column — the value never comes from a result column
// — but every consumer downstream reads only Field, so a param and a column ask
// the backend for their distinct values through one path. The key is the bare
// param name: the "filter." prefix is what routes a request key to the column
// table, and a param's key must stay the name its query-string entry uses.
func (p Profile) ParamFilterBindings() []ColumnFilterBinding {
	bindings := make([]ColumnFilterBinding, 0, len(p.Params))
	for _, param := range p.Params {
		if param.Type != ParamTypeList || param.Field == "" {
			continue
		}
		bindings = append(bindings, ColumnFilterBinding{
			Key: param.Name, Field: param.Field, Label: param.DisplayLabel(),
		})
	}
	return bindings
}

// filterBinding resolves a lookup key against the column and param bindings a
// profile offers.
func (p Profile) filterBinding(key string) (ColumnFilterBinding, error) {
	columns, err := p.ColumnFilterBindings()
	if err != nil {
		return ColumnFilterBinding{}, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	for _, binding := range append(columns, p.ParamFilterBindings()...) {
		if binding.Key == key {
			return binding, nil
		}
	}
	return ColumnFilterBinding{}, fmt.Errorf("filter %q is not supported by profile %q", key, p.Name)
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

// resolveProfileInput turns one request's input into the values exposed to the
// query template and the native include/exclude clauses the provider applies.
// Column filters (filter.<column>) and tri-state list params both land in the
// same []ColumnFilterValue, so an exclusion has exactly one transport whichever
// end of the profile declared it. Column filters come first, then params in
// declaration order, so a request builds the same body every time.
func resolveProfileInput(profile Profile, input map[string]any) (map[string]any, []ColumnFilterValue, error) {
	profileParams, filters, err := partitionProfileInput(profile, input)
	if err != nil {
		return nil, nil, err
	}
	resolved, paramFilters, err := resolveParams(profile.Params, profileParams)
	if err != nil {
		return nil, nil, err
	}
	return resolved, append(filters, paramFilters...), nil
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
	resolved, filters, err := resolveProfileInput(profile, input)
	if err != nil {
		return nil, 0, fmt.Errorf("profile %q: %w", profile.Name, err)
	}
	binding, err := profile.filterBinding(key)
	if err != nil {
		return nil, 0, err
	}
	// The filter being looked up must not narrow its own options, or a chosen
	// value would hide every alternative. Every other active selection — column
	// or param — still scopes the question.
	siblings := make([]ColumnFilterValue, 0, len(filters))
	for _, filter := range filters {
		if filter.Key != key {
			siblings = append(siblings, filter)
		}
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
	return lookup.LookupFilterValues(ctx, req, binding, search, limit)
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
