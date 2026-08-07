package query

import (
	"fmt"
	"strings"
)

// ColumnFilterKind is the control a column filter offers and the grammar its
// wire value is read under. It is derived from ColumnType and overridable per
// column, because the shape a value renders in and the shape the backend
// compares it in are different questions: an HTTP status renders as a badge and
// is stored as a number.
type ColumnFilterKind string

const (
	// ColumnFilterKindTerms selects exact values, with exclusions. It is what a
	// column filters by unless its type says otherwise, and the only kind whose
	// values the backend can be asked to enumerate.
	ColumnFilterKindTerms ColumnFilterKind = "terms"

	// ColumnFilterKindRange bounds a numeric field from below, above, or both.
	ColumnFilterKindRange ColumnFilterKind = "range"

	// ColumnFilterKindTime bounds a date field. Its operands are RFC3339
	// instants or date math ("now-15m"); OpenSearch resolves those itself and
	// SQL resolves them at bind time.
	ColumnFilterKindTime ColumnFilterKind = "time"

	// ColumnFilterKindBoolean is a yes/no/any toggle. Unset is the third arm and
	// is not the same as false: a row missing the value is neither.
	ColumnFilterKindBoolean ColumnFilterKind = "boolean"

	// ColumnFilterKindText matches a substring rather than a whole value. It is
	// what an analyzed field with no exact-value sibling can offer, and it is
	// never inferred — a column asks for it explicitly.
	ColumnFilterKindText ColumnFilterKind = "text"

	// ColumnFilterKindNone marks a column that offers no filter. It is what a
	// structured type infers, and what an author sets to suppress one.
	ColumnFilterKindNone ColumnFilterKind = "none"
)

// Normalized resolves the zero value, which means a value selection. Spelling
// the common case out in every literal would buy nothing.
func (k ColumnFilterKind) Normalized() ColumnFilterKind {
	if k == "" {
		return ColumnFilterKindTerms
	}
	return k
}

// Valid reports whether k names a kind this package compiles.
func (k ColumnFilterKind) Valid() bool {
	switch k.Normalized() {
	case ColumnFilterKindTerms, ColumnFilterKindRange, ColumnFilterKindTime,
		ColumnFilterKindBoolean, ColumnFilterKindText, ColumnFilterKindNone:
		return true
	default:
		return false
	}
}

// Lookupable reports whether the backend can be asked for this filter's values.
// Only a value selection has a list to offer; a range, a toggle and a substring
// are typed, not picked.
func (k ColumnFilterKind) Lookupable() bool {
	return k.Normalized() == ColumnFilterKindTerms
}

// ControlType is the clicky filter type this kind registers as, which decides
// the FilterBar control the browser renders. The vocabulary is clicky's own
// (entity.FilterSpec.Type), not one this package invents. An empty result means
// the filter needs no named component and travels as a plain parameter.
//
// A time bound is "date-range" rather than "date": its wire value carries both
// edges under one key, and "date" is clicky's single-instant input.
//
// Callers with a binding in hand should ask it instead — whether a value
// selection is picked or typed is not a property of the kind alone.
func (k ColumnFilterKind) ControlType() string {
	switch k.Normalized() {
	case ColumnFilterKindTerms:
		return "multi-filter"
	case ColumnFilterKindRange:
		return "number"
	case ColumnFilterKindTime:
		return "date-range"
	case ColumnFilterKindBoolean:
		return "bool"
	default:
		return ""
	}
}

// ColumnFilterKindValues returns every kind an author may declare, for the
// profile schema's enum.
func ColumnFilterKindValues() []string {
	return []string{
		string(ColumnFilterKindTerms),
		string(ColumnFilterKindRange),
		string(ColumnFilterKindTime),
		string(ColumnFilterKindBoolean),
		string(ColumnFilterKindText),
		string(ColumnFilterKindNone),
	}
}

// columnFilterKindFor is the inference: what a column filters by when it says
// nothing more. Text is absent on purpose — a substring match is a choice an
// author makes, never one a type implies.
func columnFilterKindFor(columnType ColumnType) ColumnFilterKind {
	switch columnType {
	case ColumnTypeNumber, ColumnTypeDuration, ColumnTypeBytes:
		return ColumnFilterKindRange
	case ColumnTypeDateTime:
		return ColumnFilterKindTime
	case ColumnTypeBoolean:
		return ColumnFilterKindBoolean
	case ColumnTypeKeyValue, ColumnTypeKeyValues, ColumnTypeJSON:
		return ColumnFilterKindNone
	default:
		return ColumnFilterKindTerms
	}
}

// resolveColumnFilterBinding builds the binding one column contributes, or
// reports that it contributes none.
//
// It is the one place the four inference rules live — filterable at all, which
// backend field, which control, where the values come from — so validation, the
// OpenAPI surface and the provider can never disagree about what a column
// offers.
func resolveColumnFilterBinding(profile Profile, column ColumnDef) (ColumnFilterBinding, bool, error) {
	if column.Hidden {
		return ColumnFilterBinding{}, false, nil
	}
	def := column.Filter
	if def != nil && def.Disabled {
		return ColumnFilterBinding{}, false, nil
	}
	kind := columnFilterKindFor(column.Type)
	if def != nil && def.Kind != "" {
		kind = def.Kind
	}
	if !kind.Valid() {
		return ColumnFilterBinding{}, false, fmt.Errorf("column %q filter kind %q is unsupported", column.Name, kind)
	}
	if kind.Normalized() == ColumnFilterKindNone {
		return ColumnFilterBinding{}, false, nil
	}

	field, declared, ok, err := columnFilterField(column)
	if err != nil {
		return ColumnFilterBinding{}, false, err
	}
	if !ok {
		return ColumnFilterBinding{}, false, nil
	}
	// A declared field is taken as written; only an inferred one still passes
	// through the provider's own naming.
	if declared {
		if err := validateSQLFilterField(profile.Provider.Type, fmt.Sprintf("column %q", column.Name), field); err != nil {
			return ColumnFilterBinding{}, false, err
		}
	} else {
		field = openTelemetryFilterField(profile, field)
		// An inferred field a SQL backend could not name simply offers no
		// filter, rather than failing a profile that renders perfectly well.
		if validateSQLFilterField(profile.Provider.Type, "", field) != nil {
			return ColumnFilterBinding{}, false, nil
		}
	}

	label := column.Label
	if label == "" {
		label = column.Name
	}
	binding := ColumnFilterBinding{
		Column: column.Name,
		Key:    columnFilterPrefix + column.Name,
		Field:  field,
		Label:  label,
		Kind:   kind.Normalized(),
		// Only a value selection holds several values. A range, a time bound, a
		// toggle and a substring are each one control writing one operand, and
		// announcing them as multi is what made the browser render them as a
		// comma-separated list of values to type.
		Multi:  kind.Normalized() == ColumnFilterKindTerms,
		Lookup: kind.Lookupable() && column.Type.Enumerable(),
	}
	if def != nil {
		binding.Options = def.Options
		if def.Multi != nil {
			binding.Multi = *def.Multi
		}
		if def.Lookup != nil {
			binding.Lookup = *def.Lookup
		}
		if def.Limit != nil {
			binding.Limit = *def.Limit
		}
	}
	// Enumerated values are the answer the lookup would go and fetch, so asking
	// for them again would be a round trip whose result is already here.
	if len(binding.Options) > 0 {
		binding.Lookup = false
	}
	if !binding.Kind.Lookupable() {
		binding.Lookup = false
	}
	return binding, true, nil
}

// validateFilterOptions rejects an enumerated value the wire form cannot carry.
// A selection is a comma-separated list whose "!" prefix means exclude, so a
// value containing either is a value no request could ever name.
func validateFilterOptions(options []string) error {
	for _, option := range options {
		if strings.TrimSpace(option) == "" {
			return fmt.Errorf("option must not be empty")
		}
		if strings.Contains(option, ",") {
			return fmt.Errorf("option %q must not contain a comma", option)
		}
		if strings.HasPrefix(option, "!") {
			return fmt.Errorf("option %q must not start with !", option)
		}
	}
	return nil
}
