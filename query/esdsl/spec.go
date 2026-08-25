// Package esdsl models an OpenSearch search as a structured specification and
// compiles it to a Query DSL request body. The specification — not hand-written
// JSON — is what profiles and the query builder store, so parameters are bound
// structurally instead of being interpolated into a query string.
package esdsl

import (
	"encoding/json"
	"fmt"
)

// Search is the structured search specification stored at provider.options.search.
type Search struct {
	// Query is the root condition. Nil selects every document.
	Query *Condition `json:"query,omitempty"`

	// Sort orders the hits. Empty leaves the backend default (_score).
	Sort []SortBy `json:"sort,omitempty"`

	// Size caps the returned hits. A limit-role parameter overrides it.
	Size *int `json:"size,omitempty"`

	// From skips the first N hits. Rejected in a scroll context.
	From *int `json:"from,omitempty"`

	// Source selects which _source fields are returned.
	Source *Source `json:"source,omitempty"`

	// TrackTotalHits controls whether the backend counts beyond 10k.
	TrackTotalHits *TrackTotalHits `json:"trackTotalHits,omitempty"`

	// StoredFields and Fields request non-_source values on each hit.
	StoredFields []string `json:"storedFields,omitempty"`
	Fields       []string `json:"fields,omitempty"`

	// Aggregations are preserved verbatim on round-trip. The builder does not
	// edit them; hand-authored aggregations survive a save from the UI.
	Aggregations map[string]json.RawMessage `json:"aggregations,omitempty"`

	// TimeField is the date field that time-from/time-to parameters fold into.
	TimeField string `json:"timeField,omitempty"`

	// TimeFieldFormat declares how a numeric TimeField stores instants. Empty
	// lets a mapped date/date_nanos field use its native date representation.
	TimeFieldFormat TimeFieldFormat `json:"timeFieldFormat,omitempty"`
}

// TimeFieldFormat is the explicit epoch unit for a numeric time field.
type TimeFieldFormat string

const (
	TimeFieldFormatEpochSecond TimeFieldFormat = "epoch_second"
	TimeFieldFormatEpochMillis TimeFieldFormat = "epoch_millis"
	TimeFieldFormatEpochMicros TimeFieldFormat = "epoch_micros"
	TimeFieldFormatEpochNanos  TimeFieldFormat = "epoch_nanos"
)

// TimeFieldFormats lists the supported numeric timestamp encodings.
func TimeFieldFormats() []TimeFieldFormat {
	return []TimeFieldFormat{
		TimeFieldFormatEpochSecond,
		TimeFieldFormatEpochMillis,
		TimeFieldFormatEpochMicros,
		TimeFieldFormatEpochNanos,
	}
}

// Occur is the bool clause a condition contributes to. Empty means filter.
type Occur string

const (
	OccurFilter  Occur = "filter"
	OccurMust    Occur = "must"
	OccurShould  Occur = "should"
	OccurMustNot Occur = "must_not"
)

// Occurs lists the bool clauses in the order they are emitted.
func Occurs() []Occur { return []Occur{OccurFilter, OccurMust, OccurShould, OccurMustNot} }

func (o Occur) normalized() Occur {
	if o == "" {
		return OccurFilter
	}
	return o
}

func (o Occur) valid() bool {
	switch o.normalized() {
	case OccurFilter, OccurMust, OccurShould, OccurMustNot:
		return true
	default:
		return false
	}
}

// Condition is one node of the query tree: either a leaf operator on a field or
// a bool/nested group over child conditions.
type Condition struct {
	// Occur is the parent bool clause this condition contributes to.
	Occur Occur `json:"occur,omitempty"`

	// Op selects the operator. See Catalog for the supported set.
	Op Operator `json:"op"`

	// Field is the target field for single-field operators.
	Field string `json:"field,omitempty"`

	// Fields targets multi_match, query_string and simple_query_string.
	Fields []string `json:"fields,omitempty"`

	// Value carries the single operand; Values carries the operand list.
	Value  *Value  `json:"value,omitempty"`
	Values []Value `json:"values,omitempty"`

	// Gt, Gte, Lt and Lte bound a range. Date fields accept date math.
	Gt  *Value `json:"gt,omitempty"`
	Gte *Value `json:"gte,omitempty"`
	Lt  *Value `json:"lt,omitempty"`
	Lte *Value `json:"lte,omitempty"`

	// Format and TimeZone qualify a range over a date field.
	Format   string `json:"format,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`

	// Analyzer overrides the search analyzer for analyzed operators.
	Analyzer string `json:"analyzer,omitempty"`

	// MatchOperator is "and" or "or" for match and simple_query_string.
	MatchOperator string `json:"matchOperator,omitempty"`

	// MultiMatchType selects the multi_match strategy (best_fields, phrase, …).
	MultiMatchType string `json:"multiMatchType,omitempty"`

	// Fuzziness is an edit distance or "AUTO".
	Fuzziness string `json:"fuzziness,omitempty"`

	// Slop allows transposed terms in a phrase match.
	Slop *int `json:"slop,omitempty"`

	// Boost weights this condition's contribution to _score.
	Boost *float64 `json:"boost,omitempty"`

	// CaseInsensitive applies to term, prefix, wildcard and regexp.
	CaseInsensitive *bool `json:"caseInsensitive,omitempty"`

	// Escape controls Lucene escaping of a parameter-sourced query_string
	// operand. It defaults to true and is always a specification literal, so a
	// supplied parameter can never turn escaping off.
	Escape *bool `json:"escape,omitempty"`

	// Path and ScoreMode configure a nested group.
	Path      string `json:"path,omitempty"`
	ScoreMode string `json:"scoreMode,omitempty"`

	// MinimumShouldMatch overrides the should-clause requirement on a bool.
	MinimumShouldMatch string `json:"minimumShouldMatch,omitempty"`

	// Conditions are the children of a bool or nested group.
	Conditions []Condition `json:"conditions,omitempty"`

	// Optional drops this condition when its parameter resolves to nothing,
	// instead of failing.
	Optional bool `json:"optional,omitempty"`

	// When names a parameter that gates this condition: it is emitted only when
	// that parameter resolves to a non-empty value, whatever the value is. It is
	// the structural form of a toggle filter, and gates a whole group when set on
	// a bool or nested condition.
	When string `json:"when,omitempty"`
}

// SortBy is one entry of the sort array.
type SortBy struct {
	Field        string `json:"field"`
	Order        string `json:"order,omitempty"`
	Mode         string `json:"mode,omitempty"`
	Missing      string `json:"missing,omitempty"`
	UnmappedType string `json:"unmappedType,omitempty"`
}

// Source selects the returned _source fields. Enabled=false disables _source
// entirely; includes/excludes narrow it.
type Source struct {
	Enabled  *bool    `json:"enabled,omitempty"`
	Includes []string `json:"includes,omitempty"`
	Excludes []string `json:"excludes,omitempty"`
}

// UnmarshalJSON accepts the three shapes OpenSearch itself accepts for
// _source: a boolean, a field list, or an includes/excludes object.
func (s *Source) UnmarshalJSON(data []byte) error {
	var enabled bool
	if err := json.Unmarshal(data, &enabled); err == nil {
		*s = Source{Enabled: &enabled}
		return nil
	}
	var includes []string
	if err := json.Unmarshal(data, &includes); err == nil {
		*s = Source{Includes: includes}
		return nil
	}
	type sourceObject Source
	var object sourceObject
	if err := json.Unmarshal(data, &object); err != nil {
		return fmt.Errorf("source must be a boolean, a field list, or an includes/excludes object: %w", err)
	}
	*s = Source(object)
	return nil
}

// TrackTotalHits is either a boolean or a counting threshold.
type TrackTotalHits struct {
	Enabled   *bool `json:"enabled,omitempty"`
	Threshold *int  `json:"threshold,omitempty"`
}

// UnmarshalJSON accepts a boolean, a number, or an object.
func (t *TrackTotalHits) UnmarshalJSON(data []byte) error {
	var enabled bool
	if err := json.Unmarshal(data, &enabled); err == nil {
		*t = TrackTotalHits{Enabled: &enabled}
		return nil
	}
	var threshold int
	if err := json.Unmarshal(data, &threshold); err == nil {
		*t = TrackTotalHits{Threshold: &threshold}
		return nil
	}
	type trackObject TrackTotalHits
	var object trackObject
	if err := json.Unmarshal(data, &object); err != nil {
		return fmt.Errorf("trackTotalHits must be a boolean, a number, or an object: %w", err)
	}
	*t = TrackTotalHits(object)
	return nil
}

// value renders the track_total_hits body value, or nil when unset.
func (t *TrackTotalHits) value() any {
	switch {
	case t == nil:
		return nil
	case t.Threshold != nil:
		return *t.Threshold
	case t.Enabled != nil:
		return *t.Enabled
	default:
		return nil
	}
}

// Value is an operand: either a literal or a reference to a profile parameter.
// A parameter is substituted structurally — never concatenated into a query
// string — which is what makes a compiled specification injection-proof.
//
// A profile parameter can also reach an operand the other way, as
// `"{{.params.country}}-api"` in a string literal: the engine interpolates the
// provider options before the specification is decoded, so the value arrives
// here already substituted. The two forms are deliberately different, and an
// author picks between them:
//
//	{"param": "country"}       substituted here, Lucene-escaped in query_string,
//	                           and prunes an optional condition when empty
//	"{{.params.country}}-api"  interpolated verbatim, exactly like a raw query,
//	                           and fails when the parameter has no value
type Value struct {
	Literal any
	Param   string
}

// Literal builds a literal operand.
func Literal(v any) *Value { return &Value{Literal: v} }

// Param builds a parameter-backed operand.
func Param(name string) *Value { return &Value{Param: name} }

// UnmarshalJSON reads {"param":"name"} as a parameter reference,
// {"literal":x} as an explicit literal, and anything else as a bare literal.
func (v *Value) UnmarshalJSON(data []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err == nil {
		if raw, ok := object["param"]; ok && len(object) == 1 {
			var name string
			if err := json.Unmarshal(raw, &name); err != nil {
				return fmt.Errorf("param reference must be a string: %w", err)
			}
			if name == "" {
				return fmt.Errorf("param reference must not be empty")
			}
			*v = Value{Param: name}
			return nil
		}
		if raw, ok := object["literal"]; ok && len(object) == 1 {
			var literal any
			if err := json.Unmarshal(raw, &literal); err != nil {
				return err
			}
			*v = Value{Literal: literal}
			return nil
		}
	}
	var literal any
	if err := json.Unmarshal(data, &literal); err != nil {
		return err
	}
	*v = Value{Literal: literal}
	return nil
}

// MarshalJSON is the inverse of UnmarshalJSON. A literal that would itself read
// back as a reference is wrapped in {"literal":…}.
func (v Value) MarshalJSON() ([]byte, error) {
	if v.Param != "" {
		return json.Marshal(map[string]string{"param": v.Param})
	}
	if object, ok := v.Literal.(map[string]any); ok && len(object) == 1 {
		if _, isParam := object["param"]; isParam {
			return json.Marshal(map[string]any{"literal": v.Literal})
		}
		if _, isLiteral := object["literal"]; isLiteral {
			return json.Marshal(map[string]any{"literal": v.Literal})
		}
	}
	return json.Marshal(v.Literal)
}
