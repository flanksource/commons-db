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

	// ColumnFilterKindExact matches whole values like terms, and is never
	// offered as a list. It is what an identifier gets: enumerating one is a
	// scan of the whole result that answers with a page of the rows, so the
	// values are typed rather than picked. Same wire grammar as terms — a
	// comma-separated list whose "!" prefix excludes.
	ColumnFilterKindExact ColumnFilterKind = "exact"

	// ColumnFilterKindRange bounds a numeric field from below, above, or both.
	ColumnFilterKindRange ColumnFilterKind = "range"

	// ColumnFilterKindDuration bounds an elapsed-time field. Its operands are
	// Go duration strings ("500ms", "2m30s") or bare numbers, and both resolve
	// to the unit the column stores — ColumnDef.Unit, milliseconds when unset.
	// A bare number is already in that unit and passes through unchanged, so a
	// bound written before this kind existed still means what it did.
	ColumnFilterKindDuration ColumnFilterKind = "duration"

	// ColumnFilterKindTime bounds a date-and-time field. Its operands are
	// RFC3339 instants or date math ("now-15m"); OpenSearch resolves those
	// itself and SQL resolves them at bind time.
	ColumnFilterKindTime ColumnFilterKind = "time"

	// ColumnFilterKindDate bounds a date field to whole days. Its operands and
	// its compiled clause are a time bound's exactly; only the control differs,
	// because a field nobody reads the clock off is a calendar rather than a
	// timestamp. It is never inferred — a datetime column infers time.
	ColumnFilterKindDate ColumnFilterKind = "date"

	// ColumnFilterKindBoolean is a yes/no/any toggle. Unset is the third arm and
	// is not the same as false: a row missing the value is neither.
	ColumnFilterKindBoolean ColumnFilterKind = "boolean"

	// ColumnFilterKindText matches a substring rather than a whole value. It is
	// what an analyzed field with no exact-value sibling can offer, and it is
	// never inferred — a column asks for it explicitly.
	ColumnFilterKindText ColumnFilterKind = "text"

	// ColumnFilterKindWorkload selects one Kubernetes workload inside the broad
	// target scope declared by a profile query.
	ColumnFilterKindWorkload ColumnFilterKind = "workload"

	// ColumnFilterKindLabels selects Kubernetes label key/value pairs, grouped
	// by key in the browser and compiled into the target selector.
	ColumnFilterKindLabels ColumnFilterKind = "labels"

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
	case ColumnFilterKindTerms, ColumnFilterKindExact, ColumnFilterKindText,
		ColumnFilterKindRange, ColumnFilterKindDuration,
		ColumnFilterKindTime, ColumnFilterKindDate,
		ColumnFilterKindBoolean, ColumnFilterKindWorkload,
		ColumnFilterKindLabels, ColumnFilterKindNone:
		return true
	default:
		return false
	}
}

// CompilesAs is the kind whose backend clause this kind produces, and the only
// question a provider has to ask. An exact match and a value selection differ
// in how the browser offers them, not in the predicate they compile to, and a
// switch that told them apart would be two arms doing one thing — and would
// refuse to merge a list param with an identifier column bound to one field.
//
// Callers deciding a control or a grammar must not use it: whether a bound is
// written "5s" or "now-1h" is exactly what this throws away.
func (k ColumnFilterKind) CompilesAs() ColumnFilterKind {
	switch normalized := k.Normalized(); normalized {
	case ColumnFilterKindExact:
		return ColumnFilterKindTerms
	case ColumnFilterKindDuration:
		return ColumnFilterKindRange
	case ColumnFilterKindDate:
		return ColumnFilterKindTime
	default:
		return normalized
	}
}

// Lookupable reports whether the backend can be asked for this filter's values.
// Only a value selection has a list to offer; a range, a toggle, a substring
// and an exact match are typed, not picked — the last because the values it
// compares are identifiers rather than a vocabulary.
func (k ColumnFilterKind) Lookupable() bool {
	switch k.Normalized() {
	case ColumnFilterKindTerms, ColumnFilterKindWorkload, ColumnFilterKindLabels:
		return true
	default:
		return false
	}
}

// ControlType is the clicky filter type this kind registers as, which decides
// the FilterBar control the browser renders. The vocabulary is clicky's own
// (entity.FilterSpec.Type), not one this package invents. An empty result means
// the filter needs no named component and travels as a plain parameter.
//
// A time bound is "date-range" rather than "date": its wire value carries both
// edges under one key, and "date" is clicky's single-instant input. A date
// bound is "day-range", the same control with the clock taken off it.
//
// Callers with a binding in hand should ask it instead — whether a value
// selection is picked or typed is not a property of the kind alone.
func (k ColumnFilterKind) ControlType() string {
	switch k.Normalized() {
	case ColumnFilterKindTerms:
		return "multi-filter"
	case ColumnFilterKindExact:
		return "value"
	case ColumnFilterKindRange:
		return "number"
	case ColumnFilterKindDuration:
		return "duration"
	case ColumnFilterKindTime:
		return "date-range"
	case ColumnFilterKindDate:
		return "day-range"
	case ColumnFilterKindBoolean:
		return "bool"
	case ColumnFilterKindWorkload:
		return "workload"
	case ColumnFilterKindLabels:
		return "labels"
	default:
		return ""
	}
}

// ColumnFilterKindValues returns every kind an author may declare, for the
// profile schema's enum. The order is the order the profile editor offers
// them, grouped by family so that a reader reaching for "duration" finds it
// beside "range" rather than wherever it happened to be appended.
func ColumnFilterKindValues() []string {
	return []string{
		string(ColumnFilterKindTerms),
		string(ColumnFilterKindExact),
		string(ColumnFilterKindText),
		string(ColumnFilterKindRange),
		string(ColumnFilterKindDuration),
		string(ColumnFilterKindDate),
		string(ColumnFilterKindTime),
		string(ColumnFilterKindBoolean),
		string(ColumnFilterKindNone),
	}
}

// columnFilterKindFor is the inference: what a column filters by when it says
// nothing more. Text and date are absent on purpose — a substring match and a
// day-granularity bound are choices an author makes, never ones a type
// implies; every type that could take one has an exact comparison or a full
// instant available already.
//
// Bytes stays a plain range rather than joining duration: a duration literal
// has one grammar, but a size literal has two bases (1024 and 1000, which
// clicky distinguishes), so "5MB" would mean different numbers depending on a
// unit the author may never have set.
func columnFilterKindFor(columnType ColumnType) ColumnFilterKind {
	switch columnType {
	case ColumnTypeNumber, ColumnTypeBytes:
		return ColumnFilterKindRange
	case ColumnTypeDuration:
		return ColumnFilterKindDuration
	case ColumnTypeDateTime:
		return ColumnFilterKindTime
	case ColumnTypeBoolean:
		return ColumnFilterKindBoolean
	case ColumnTypeUUID:
		return ColumnFilterKindExact
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
	kind = kind.Normalized()
	if kind == ColumnFilterKindNone {
		return ColumnFilterBinding{}, false, nil
	}
	// A duration bound is resolved into the unit the column stores, so a unit
	// nothing can convert into is refused where it was written rather than on
	// the first request that types "5s".
	if kind == ColumnFilterKindDuration {
		if _, err := durationUnitScale(column.Unit); err != nil {
			return ColumnFilterBinding{}, false, fmt.Errorf("column %q: %w", column.Name, err)
		}
	}

	target, declared, ok, err := columnFilterTarget(column)
	if err != nil {
		return ColumnFilterBinding{}, false, err
	}
	if !ok {
		return ColumnFilterBinding{}, false, nil
	}
	nested, where, nestable, err := resolveColumnNesting(column, target)
	if err != nil {
		return ColumnFilterBinding{}, false, err
	}
	// A path that picks an entry of a container nobody declared as nested cannot
	// be pushed down at all — see resolveColumnNesting.
	if !nestable {
		return ColumnFilterBinding{}, false, nil
	}
	owner := fmt.Sprintf("column %q", column.Name)
	if err := validateNestedProvider(profile.Provider.Type, owner, nested); err != nil {
		return ColumnFilterBinding{}, false, err
	}
	field := target.Field
	// A declared field is taken as written; only an inferred one still passes
	// through the provider's own naming.
	if declared {
		if err := validateSQLFilterField(profile.Provider.Type, owner, field); err != nil {
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
	if err := validateNestedField(column.Name, field, nested); err != nil {
		return ColumnFilterBinding{}, false, err
	}

	label := column.Label
	if label == "" {
		label = column.Name
	}
	binding := ColumnFilterBinding{
		Column: column.Name,
		Key:    columnFilterPrefix + column.Name,
		Field:  field,
		Nested: nested,
		Where:  where,
		Label:  label,
		Kind:   kind,
		Unit:   column.Unit,
		// Only a value selection holds several values. A range, a time bound, a
		// toggle and a substring are each one control writing one operand, and
		// announcing them as multi is what made the browser render them as a
		// comma-separated list of values to type. An exact match is a value
		// selection that happens to have no list, so it still takes several.
		Multi:  kind == ColumnFilterKindTerms || kind == ColumnFilterKindExact,
		Lookup: kind.Lookupable() && column.Type.Enumerable(),
	}
	if def != nil {
		if err := assertLookupableDeclaration(column.Name, kind, *def); err != nil {
			return ColumnFilterBinding{}, false, err
		}
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
	return binding, true, nil
}

// assertLookupableDeclaration refuses an option list, a lookup or a limit on a
// kind that has no values to offer.
//
// ColumnFilterDef.Validate cannot ask this: it sees the author's words and not
// the column's type, so an identifier column with no declared kind reads as a
// value selection there and resolves to an exact match here. Ignoring the
// declaration is what this used to do, and it turned "I asked for a dropdown"
// into "there is no dropdown, and nothing said why".
//
// An explicit lookup:false is allowed through — it states what the resolution
// already concluded, so it is agreement rather than a mistake.
func assertLookupableDeclaration(column string, kind ColumnFilterKind, def ColumnFilterDef) error {
	if kind.Lookupable() {
		return nil
	}
	switch {
	case len(def.Options) > 0:
		return fmt.Errorf(
			"column %q filters by %s, which has no value list to enumerate; drop options or declare kind: %s",
			column, kind, ColumnFilterKindTerms)
	case def.Lookup != nil && *def.Lookup:
		return fmt.Errorf(
			"column %q filters by %s, whose values are typed rather than picked; declare kind: %s to enumerate them",
			column, kind, ColumnFilterKindTerms)
	case def.Limit != nil:
		return fmt.Errorf(
			"column %q filters by %s, which has no value list to cap; drop limit or declare kind: %s",
			column, kind, ColumnFilterKindTerms)
	}
	return nil
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
