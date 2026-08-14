package query

import (
	"fmt"
	"regexp"
	"strings"
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
// is an override: a direct column, one whose CEL is a plain row lookup, or one
// whose JSONPath is a literal key chain infers its field from the column itself
// and its control from Type. This exists for the columns inference cannot
// reach — a computed CEL, or a JSONPath that selects rather than addresses —
// and for the ones where inference has the shape right and the backend wrong.
type ColumnFilterDef struct {
	// Field is the backend field the selection is applied to. For a document
	// store it is the indexed field; for SQL it is the result column the query
	// returns. Required only when the column's own definition implies none.
	Field string `json:"field,omitempty" yaml:"field,omitempty"`

	// Nested names the `nested` mapping Field lives inside. A document store
	// indexes each element of such a field as its own document, so a selection on
	// one has to be compiled inside a nested query; a flat clause on it matches no
	// document at all, and says nothing about why.
	//
	// It cannot be inferred from the column, because a tag list mapped `nested`
	// and a plain array of objects report identical fields — only the index
	// mapping tells them apart. Declare it for a profile; the connection browser
	// reads it from the mapping itself.
	Nested string `json:"nested,omitempty" yaml:"nested,omitempty"`

	// Where pins the constants the selection also requires, keyed by backend
	// field. It is what narrows to one entry of a key/value tag list: the key is
	// fixed here and the value is what the operator picks.
	//
	// It requires Nested. Outside a nested query the two clauses are ANDed across
	// the whole document, which matches a document carrying the key on one entry
	// and the value on another — the wrong rows, returned confidently.
	Where map[string]string `json:"where,omitempty" yaml:"where,omitempty"`

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
	if err := d.validateNesting(column); err != nil {
		return err
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

// validateNesting rejects a nesting declaration no backend could apply. The
// field/container relationship is checked here rather than at binding time
// because both sides are the author's own words; the inferred field is checked
// against them later, once it is known.
func (d ColumnFilterDef) validateNesting(column string) error {
	nested := strings.TrimSpace(d.Nested)
	if nested == "" {
		if len(d.Where) > 0 {
			return fmt.Errorf(
				"column %q filter sets where without nested; outside a nested query the constants are matched against the whole document rather than one entry of it",
				column)
		}
		return nil
	}
	if field := strings.TrimSpace(d.Field); field != "" && !underNested(field, nested) {
		return fmt.Errorf("column %q filter field %q is not inside nested %q", column, field, nested)
	}
	for field, value := range d.Where {
		if !underNested(field, nested) {
			return fmt.Errorf("column %q filter where field %q is not inside nested %q", column, field, nested)
		}
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("column %q filter where field %q pins an empty value", column, field)
		}
	}
	return nil
}

// underNested reports whether field is addressed through container. A document
// store names a nested field's members by prefix, so the prefix is the whole
// test — and the container itself is not one of its own members.
func underNested(field, container string) bool {
	return strings.HasPrefix(field, container+".")
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
	// Unit is the unit the column stores its values in (ColumnDef.Unit), which
	// a duration bound is resolved into. Empty means milliseconds, the unit an
	// unannotated duration column is read under. No other kind consults it.
	Unit string
	// Nested and Where carry the container the selection is compiled inside and
	// the constants that address one entry of it. See ColumnFilterDef.
	Nested string
	Where  map[string]string
	// Limit is the author's declared cap on the lookup, or zero when they
	// declared none. Zero is not "no values": it is what leaves the choice to
	// whoever asks, which is why an inferred binding never fills it in.
	Limit int
}

// ControlType is the clicky filter type this binding registers as.
//
// It refines the kind's own answer, because whether a value selection is picked
// or typed is not a property of the kind: a selection with no option list and
// nothing to enumerate would open an empty dropdown, so it asks for an input
// instead. That is what a UUID column gets — the values still compare exactly,
// they are just written rather than chosen.
func (b ColumnFilterBinding) ControlType() string {
	if b.Kind.Normalized() == ColumnFilterKindTerms && !b.Lookup && len(b.Options) == 0 {
		return "value"
	}
	return b.Kind.ControlType()
}

// FilterBound is one edge of a range. Value is carried as the kind stores it: a
// float64 for a numeric field and for a duration field (already resolved into
// the column's own unit), and for a date field the operand as written — either
// an RFC3339 instant or date math, which the backend resolves.
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

	// Nested and Where carry the container this selection is compiled inside and
	// the constants that address one entry of it. See ColumnFilterDef.
	Nested string
	Where  map[string]string

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
