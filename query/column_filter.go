package query

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/flanksource/commons-db/context"
)

const columnFilterPrefix = "filter."

const (
	// DefaultFilterLookupLimit is how many distinct values a profile's filter
	// offers before the rest must be reached by typing. It is small on purpose:
	// a list nobody can scan is not a list anyone picks from, and the values past
	// it are one debounced search away.
	DefaultFilterLookupLimit = 50

	// MaxFilterLookupLimit is the largest head a filter may ask for. It mirrors
	// clicky's entity.MaxLookupOptions, which caps the lookup response either
	// way — declaring it here is what turns a silent reduction at request time
	// into a loud rejection when the profile is written.
	MaxFilterLookupLimit = 200
)

var (
	directCELField  = regexp.MustCompile(`^(?:row|span)\.([A-Za-z_][A-Za-z0-9_.@-]*)$`)
	indexedCELField = regexp.MustCompile(`^(?:row|span)\[['"]([^'"]+)['"]\]$`)
)

// ColumnFilterDef declares how a column is filtered at the backend. Every field
// is an override: a direct column, or one whose CEL is a plain row lookup,
// infers its field from the column itself and its control from Type. This
// exists for the columns inference cannot reach — a computed CEL or a JSONPath
// value — and for the ones where inference has the shape right and the backend
// wrong.
type ColumnFilterDef struct {
	// Field is the backend field the selection is applied to. For a document
	// store it is the indexed field; for SQL it is the result column the query
	// returns. Required only when the column's own definition implies none.
	Field string `json:"field,omitempty" yaml:"field,omitempty"`

	// Kind overrides the control and the value grammar. Empty derives it from
	// Type: string, status and health select values; number, duration and bytes
	// take numeric bounds; datetime takes a time range; boolean is a yes/no
	// toggle; key_value, key_values and json offer nothing. Set it where the
	// rendered type and the backend storage disagree — a status code shown as a
	// badge but stored as a number filters by range.
	Kind ColumnFilterKind `json:"kind,omitempty" yaml:"kind,omitempty"`

	// Options enumerates the selectable values, replacing the backend lookup. It
	// is what a low-cardinality field the backend cannot aggregate needs: the
	// question the lookup asks has no answer there. Value selections only.
	Options []string `json:"options,omitempty" yaml:"options,omitempty"`

	// Lookup asks the backend for this field's distinct values. Defaults to true
	// for a value selection with no Options. Turning it off leaves the values
	// typed rather than picked, which is the only workable control over a
	// high-cardinality field like a trace id.
	Lookup *bool `json:"lookup,omitempty" yaml:"lookup,omitempty"`

	// Multi allows several values at once, defaulting to true. A single-valued
	// filter still excludes: "!eu" is one value with a sign, not two.
	Multi *bool `json:"multi,omitempty" yaml:"multi,omitempty"`

	// Limit caps how many distinct values one lookup offers, defaulting to
	// DefaultFilterLookupLimit. Everything past it is reached by typing rather
	// than scrolling, so raise it for a field whose whole range is worth seeing
	// at once and lower it for one where even fifty is noise. Value selections
	// only — the other kinds have no list to cap.
	Limit *int `json:"limit,omitempty" yaml:"limit,omitempty"`

	// Disabled offers no filter for this column while leaving the column itself
	// rendered, which Hidden does not.
	Disabled bool `json:"disabled,omitempty" yaml:"disabled,omitempty"`
}

// Validate rejects a filter declaration that cannot behave as written.
func (d ColumnFilterDef) Validate(column string) error {
	if !d.Kind.Valid() {
		return fmt.Errorf("column %q filter kind %q is unsupported", column, d.Kind)
	}
	if d.Limit != nil {
		if kind := d.Kind.Normalized(); kind != ColumnFilterKindTerms {
			return fmt.Errorf("column %q filter limit requires a %q filter, not %q", column, ColumnFilterKindTerms, kind)
		}
		if *d.Limit < 1 || *d.Limit > MaxFilterLookupLimit {
			return fmt.Errorf(
				"column %q filter limit %d is out of range; a lookup offers between 1 and %d values",
				column, *d.Limit, MaxFilterLookupLimit)
		}
	}
	if len(d.Options) == 0 {
		return nil
	}
	if kind := d.Kind.Normalized(); kind != ColumnFilterKindTerms {
		return fmt.Errorf("column %q filter options require a %q filter, not %q", column, ColumnFilterKindTerms, kind)
	}
	if err := validateFilterOptions(d.Options); err != nil {
		return fmt.Errorf("column %q filter: %w", column, err)
	}
	return nil
}

// ColumnFilterBinding is one filterable column or param as every consumer sees
// it: the request key it answers to, the backend field it applies to, and the
// control it offers.
type ColumnFilterBinding struct {
	Column  string
	Key     string
	Field   string
	Label   string
	Kind    ColumnFilterKind
	Options []string
	Lookup  bool
	Multi   bool
	// Limit is the author's declared cap on the lookup, or zero when they
	// declared none. Zero is not "no values": it is what leaves the choice to
	// whoever asks, which is why an inferred binding never fills it in.
	Limit int
}

// FilterBound is one edge of a range. Value is carried as the kind stores it: a
// float64 for a numeric field, and for a date field the operand as written —
// either an RFC3339 instant or date math, which the backend resolves.
type FilterBound struct {
	Value     any
	Inclusive bool
}

// FilterRange is a bounded selection. Either edge may be absent, leaving that
// side open; both absent is not a selection.
type FilterRange struct {
	Min *FilterBound
	Max *FilterBound
}

// ColumnFilterValue is one resolved selection on its way to a provider.
type ColumnFilterValue struct {
	Column string
	Key    string
	Field  string

	// Kind is the grammar the value was parsed under and the one a provider
	// compiles it back out of. Empty means a value selection.
	Kind ColumnFilterKind

	// Include and Exclude carry a value or substring selection.
	Include []string
	Exclude []string

	// Range carries a numeric or time selection.
	Range *FilterRange

	// Bool carries a yes/no selection. Nil is the "any" arm.
	Bool *bool
}

// IsZero reports that nothing was selected, which is the same as the filter
// being absent.
func (v ColumnFilterValue) IsZero() bool {
	return len(v.Include) == 0 && len(v.Exclude) == 0 && v.Range == nil && v.Bool == nil
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
// The SQL entries are the registry keys the sql provider registers under, not
// connection types — "sqlserver", not models.ConnectionTypeSQLServer. The
// generic "sql" key is included even though its engine is unknown until the
// connection is hydrated: a binding is dialect-agnostic, and only quoting the
// identifier needs to know which engine it is for.
//
// postgrest stays out. It has no filter builder, its filter syntax is
// PostgREST's own, and its Execute never sees a dialect — so listing it would
// advertise controls that silently do nothing, which is what this list exists
// to prevent.
func SupportsNativeFilters(providerType string) bool {
	switch providerType {
	case "opensearch", "opentelemetry",
		"sql", "postgres", "mysql", "sqlserver", "clickhouse":
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
		binding, ok, err := resolveColumnFilterBinding(p, column)
		if err != nil {
			return nil, err
		}
		if ok {
			bindings = append(bindings, binding)
		}
	}
	return bindings, nil
}

// sqlIdentifierField matches the only shape a SQL backend field may take: one
// plain column of the query's result. A SQL filter names an output column and
// quotes it, so anything that is not an identifier could not be quoted into a
// column reference at all.
var sqlIdentifierField = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,127}$`)

// validateSQLFilterField rejects a declared backend field a SQL profile could
// never apply.
//
// It runs only on a field the author wrote. An inferred one that is not an
// identifier — a column literally named "payload.user" — yields no binding and
// no error, exactly as a computed-CEL column does today, because failing there
// would break profiles that are valid and working right now.
func validateSQLFilterField(providerType, owner, field string) error {
	switch providerType {
	case "sql", "postgres", "mysql", "sqlserver", "clickhouse":
	default:
		return nil
	}
	if sqlIdentifierField.MatchString(field) {
		return nil
	}
	return fmt.Errorf(
		"%s filter field %q is not a plain column name; a SQL filter narrows one column of the query's result", owner, field)
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
			Kind: ColumnFilterKindTerms, Lookup: true, Multi: true,
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

// columnFilterField infers the backend field a column filters on and reports
// whether the profile declared it outright. A declared field is taken as
// written; an inferred one still passes through the provider's own naming.
//
// A column whose value is computed — a CEL expression that is not a plain row
// lookup, a JSONPath — resolves to nothing and is silently left unfilterable.
// The value exists only after the row was read, so there is no backend field to
// push the selection down to, and an author who wants one says so with
// filter.field.
func columnFilterField(column ColumnDef) (field string, declared bool, ok bool, err error) {
	if column.Filter != nil {
		if declaredField := strings.TrimSpace(column.Filter.Field); declaredField != "" {
			return declaredField, true, true, nil
		}
	}
	if column.Source != "" {
		return column.Source, false, true, nil
	}
	expression := strings.TrimSpace(column.CEL)
	if expression == "" {
		if column.Name == "" {
			return "", false, false, fmt.Errorf("column filter requires a name or an explicit filter field")
		}
		return column.Name, false, true, nil
	}
	if matches := directCELField.FindStringSubmatch(expression); len(matches) == 2 {
		return matches[1], false, true, nil
	}
	if matches := indexedCELField.FindStringSubmatch(expression); len(matches) == 2 {
		return matches[1], false, true, nil
	}
	return "", false, false, nil
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
		selection, err := parseColumnFilterSelection(binding.Kind, value)
		if err != nil {
			return nil, nil, fmt.Errorf("column filter %q: %w", key, err)
		}
		if selection.IsZero() {
			continue
		}
		selection.Column, selection.Key, selection.Field = binding.Column, binding.Key, binding.Field
		values[key] = selection
	}
	filters := make([]ColumnFilterValue, 0, len(values))
	for _, binding := range bindings {
		if value, ok := values[binding.Key]; ok {
			filters = append(filters, value)
		}
	}
	return params, filters, nil
}

func LookupFilterValues(ctx context.Context, profile Profile, input map[string]any, key, search string, limit int) ([]FilterOption, *Total, error) {
	if err := profile.Validate(); err != nil {
		return nil, nil, err
	}
	if profile.Namespace != "" {
		ctx = ctx.WithNamespace(profile.Namespace)
	}
	resolved, filters, err := resolveProfileInput(profile, input)
	if err != nil {
		return nil, nil, fmt.Errorf("profile %q: %w", profile.Name, err)
	}
	binding, err := profile.filterBinding(key)
	if err != nil {
		return nil, nil, err
	}
	if !binding.Kind.Lookupable() {
		return nil, nil, fmt.Errorf("filter %q is a %s filter and has no values to list", key, binding.Kind.Normalized())
	}
	// A declared limit narrows what the caller asked for; it never widens it.
	// The guard is on the binding rather than the caller because a binding
	// nobody declared — every inferred one, and every one the connection browser
	// builds — must keep answering the size its caller chose.
	if binding.Limit > 0 && (limit <= 0 || binding.Limit < limit) {
		limit = binding.Limit
	}
	// An enumerated filter already carries the answer, so asking the backend
	// would be a round trip whose result is sitting in the profile. It is
	// deliberately served whole: `options` names the values that exist, so
	// withholding some of them would answer a different question than the one
	// the author wrote.
	if len(binding.Options) > 0 {
		options := make([]FilterOption, 0, len(binding.Options))
		for _, option := range binding.Options {
			if search == "" || strings.Contains(strings.ToLower(option), strings.ToLower(search)) {
				options = append(options, FilterOption{Value: option})
			}
		}
		// An enumerated filter is the whole set by construction, so the count is
		// the number and not an estimate of it.
		return options, &Total{Value: int64(len(options)), Exact: true}, nil
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
		return nil, nil, err
	}
	lookup, ok := provider.(FilterLookupProvider)
	if !ok {
		return nil, nil, fmt.Errorf("provider %q does not support column filter lookups", profile.Provider.Type)
	}
	req, err := buildProviderRequest(ctx, profile.Provider, profile.Query, profile.Params, resolved)
	if err != nil {
		return nil, nil, fmt.Errorf("profile %q: %w", profile.Name, err)
	}
	req.Filters = siblings
	return lookup.LookupFilterValues(ctx, req, binding, search, limit)
}

// ResolveColumnFilters resolves filter.<column> request values against a
// profile's columns, for a caller that assembled the profile itself rather than
// loading a stored one — the connection browser, which infers its columns from
// the rows a first, unfiltered run returned. Params are not resolved here: an
// assembled profile declares none.
func ResolveColumnFilters(profile Profile, input map[string]any) ([]ColumnFilterValue, error) {
	_, filters, err := partitionProfileInput(profile, input)
	if err != nil {
		return nil, err
	}
	return filters, nil
}
