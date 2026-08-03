package esdsl

// Operator names one OpenSearch query clause.
type Operator string

const (
	OpTerm              Operator = "term"
	OpTerms             Operator = "terms"
	OpMatch             Operator = "match"
	OpMatchPhrase       Operator = "match_phrase"
	OpMatchPhrasePrefix Operator = "match_phrase_prefix"
	OpMultiMatch        Operator = "multi_match"
	OpPrefix            Operator = "prefix"
	OpWildcard          Operator = "wildcard"
	OpRegexp            Operator = "regexp"
	OpFuzzy             Operator = "fuzzy"
	OpRange             Operator = "range"
	OpExists            Operator = "exists"
	OpIDs               Operator = "ids"
	OpQueryString       Operator = "query_string"
	OpSimpleQueryString Operator = "simple_query_string"
	OpNested            Operator = "nested"
	OpBool              Operator = "bool"
	OpMatchAll          Operator = "match_all"
)

// Arity describes the operand shape an operator takes, which is what the
// builder uses to pick a value editor.
type Arity string

const (
	// ArityNone takes no operand (exists, match_all).
	ArityNone Arity = "none"
	// AritySingle takes one operand.
	AritySingle Arity = "single"
	// ArityMultiple takes a list of operands (terms, ids).
	ArityMultiple Arity = "multiple"
	// ArityRange takes any of gt/gte/lt/lte.
	ArityRange Arity = "range"
	// ArityGroup takes child conditions (bool, nested).
	ArityGroup Arity = "group"
)

// Field type families. A field's mapping type is reduced to one of these before
// operators are offered for it.
const (
	FamilyKeyword = "keyword"
	FamilyText    = "text"
	FamilyDate    = "date"
	FamilyNumber  = "number"
	FamilyBoolean = "boolean"
	FamilyIP      = "ip"
	FamilyNested  = "nested"
	// FamilyAny marks an operator that does not depend on the field type.
	FamilyAny = "any"
)

// Parameter roles a compiled specification understands. They mirror
// query.ParamRole without importing it — esdsl stays a leaf package.
const (
	RoleFilter   = "filter"
	RoleLimit    = "limit"
	RoleOffset   = "offset"
	RoleTimeFrom = "time-from"
	RoleTimeTo   = "time-to"
)

// OperatorInfo describes one operator to the query builder. Catalog is the
// single source of truth: it is emitted into the profile JSON schema, so the
// frontend never hardcodes the operator set.
type OperatorInfo struct {
	// Op is the operator name used in a Condition.
	Op Operator `json:"op"`

	// Label is the human-facing name.
	Label string `json:"label"`

	// Arity is the operand shape.
	Arity Arity `json:"arity"`

	// NeedsField marks operators that target exactly one field.
	NeedsField bool `json:"needsField"`

	// AcceptsFields marks operators that target a list of fields.
	AcceptsFields bool `json:"acceptsFields,omitempty"`

	// FieldTypes lists the field families the operator applies to.
	FieldTypes []string `json:"fieldTypes"`

	// Analyzed marks operators whose operand runs through the search analyzer,
	// so applying them to a keyword field rarely does what an author expects.
	Analyzed bool `json:"analyzed,omitempty"`

	// Group marks the operators that hold child conditions rather than operands.
	Group bool `json:"group,omitempty"`
}

var catalog = []OperatorInfo{
	{Op: OpTerm, Label: "is", Arity: AritySingle, NeedsField: true,
		FieldTypes: []string{FamilyKeyword, FamilyText, FamilyDate, FamilyNumber, FamilyBoolean, FamilyIP}},
	{Op: OpTerms, Label: "is one of", Arity: ArityMultiple, NeedsField: true,
		FieldTypes: []string{FamilyKeyword, FamilyText, FamilyDate, FamilyNumber, FamilyBoolean, FamilyIP}},
	{Op: OpMatch, Label: "matches", Arity: AritySingle, NeedsField: true,
		FieldTypes: []string{FamilyText, FamilyKeyword}, Analyzed: true},
	{Op: OpMatchPhrase, Label: "matches phrase", Arity: AritySingle, NeedsField: true,
		FieldTypes: []string{FamilyText, FamilyKeyword}, Analyzed: true},
	{Op: OpMatchPhrasePrefix, Label: "matches phrase prefix", Arity: AritySingle, NeedsField: true,
		FieldTypes: []string{FamilyText, FamilyKeyword}, Analyzed: true},
	{Op: OpMultiMatch, Label: "matches any field", Arity: AritySingle, AcceptsFields: true,
		FieldTypes: []string{FamilyText, FamilyKeyword}, Analyzed: true},
	{Op: OpPrefix, Label: "starts with", Arity: AritySingle, NeedsField: true,
		FieldTypes: []string{FamilyKeyword, FamilyText}},
	{Op: OpWildcard, Label: "matches wildcard", Arity: AritySingle, NeedsField: true,
		FieldTypes: []string{FamilyKeyword, FamilyText}},
	{Op: OpRegexp, Label: "matches regexp", Arity: AritySingle, NeedsField: true,
		FieldTypes: []string{FamilyKeyword, FamilyText}},
	{Op: OpFuzzy, Label: "is like", Arity: AritySingle, NeedsField: true,
		FieldTypes: []string{FamilyKeyword, FamilyText}},
	{Op: OpRange, Label: "is between", Arity: ArityRange, NeedsField: true,
		FieldTypes: []string{FamilyDate, FamilyNumber, FamilyIP, FamilyKeyword}},
	{Op: OpExists, Label: "exists", Arity: ArityNone, NeedsField: true,
		FieldTypes: []string{FamilyAny}},
	{Op: OpIDs, Label: "has document id", Arity: ArityMultiple,
		FieldTypes: []string{FamilyAny}},
	{Op: OpQueryString, Label: "query string", Arity: AritySingle, AcceptsFields: true,
		FieldTypes: []string{FamilyAny}, Analyzed: true},
	{Op: OpSimpleQueryString, Label: "simple query string", Arity: AritySingle, AcceptsFields: true,
		FieldTypes: []string{FamilyAny}, Analyzed: true},
	{Op: OpNested, Label: "nested", Arity: ArityGroup, FieldTypes: []string{FamilyNested}, Group: true},
	{Op: OpBool, Label: "group", Arity: ArityGroup, FieldTypes: []string{FamilyAny}, Group: true},
	{Op: OpMatchAll, Label: "everything", Arity: ArityNone, FieldTypes: []string{FamilyAny}},
}

var catalogByOp = func() map[Operator]OperatorInfo {
	index := make(map[Operator]OperatorInfo, len(catalog))
	for _, info := range catalog {
		index[info.Op] = info
	}
	return index
}()

// Enumerated qualifier vocabularies. They are emitted into the JSON schema and
// enforced by Validate, so the authoring form and the compiler never disagree
// about what a qualifier accepts.
var (
	matchOperators  = []string{"and", "or"}
	multiMatchTypes = []string{"best_fields", "most_fields", "cross_fields", "phrase", "phrase_prefix", "bool_prefix"}
	scoreModes      = []string{"avg", "sum", "min", "max", "none"}
	sortOrders      = []string{"asc", "desc"}
)

// MatchOperators, MultiMatchTypes, ScoreModes and SortOrders return the accepted
// values for the qualifiers that take a closed vocabulary.
func MatchOperators() []string  { return append([]string(nil), matchOperators...) }
func MultiMatchTypes() []string { return append([]string(nil), multiMatchTypes...) }
func ScoreModes() []string      { return append([]string(nil), scoreModes...) }
func SortOrders() []string      { return append([]string(nil), sortOrders...) }

// Catalog returns every supported operator in a stable order.
func Catalog() []OperatorInfo {
	out := make([]OperatorInfo, len(catalog))
	copy(out, catalog)
	return out
}

// Qualifiers reports, per advanced qualifier, the operators that emit it. The
// builder's advanced editor offers only the qualifiers the selected operator
// actually uses, so it needs the same table Validate enforces.
func Qualifiers() map[string][]Operator {
	out := make(map[string][]Operator, len(qualifierOperators))
	for name, ops := range qualifierOperators {
		out[name] = append([]Operator(nil), ops...)
	}
	return out
}

// QualifierNames returns the qualifier names in a stable order.
func QualifierNames() []string { return append([]string(nil), sortedQualifiers...) }

// Lookup returns the catalog entry for op.
func Lookup(op Operator) (OperatorInfo, bool) {
	info, ok := catalogByOp[op]
	return info, ok
}

// Operators returns the supported operator names in catalog order.
func Operators() []Operator {
	out := make([]Operator, 0, len(catalog))
	for _, info := range catalog {
		out = append(out, info.Op)
	}
	return out
}
