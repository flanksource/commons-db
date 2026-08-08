package providers

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
)

// decodeOpenSearchBody parses a hand-written Query DSL body. Numbers are
// decoded as json.Number so re-encoding reproduces the author's literals rather
// than rewriting them through float64.
func decodeOpenSearchBody(raw string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	body := map[string]any{}
	if err := decoder.Decode(&body); err != nil {
		return nil, fmt.Errorf("decode OpenSearch query: %w", err)
	}
	return body, nil
}

// applyOpenSearchFilters folds the resolved selections into the query as bool
// clauses. The values one field collects are alternatives, so they become a
// single terms clause — one term clause per value would AND them and match
// nothing. Distinct fields stay ANDed, and two filters bound to the same backend
// field (a column filter and a list param, say) merge into one clause.
func applyOpenSearchFilters(body map[string]any, filters []query.ColumnFilterValue, paramUses []esdsl.ParamUse) error {
	effective, err := dedupeStructuralParams(filters, paramUses)
	if err != nil {
		return err
	}
	if len(effective) == 0 {
		return nil
	}
	includes, excludes, err := openSearchFilterClauses(effective)
	if err != nil {
		return err
	}
	if len(includes) == 0 && len(excludes) == 0 {
		return nil
	}
	existing := body["query"]
	if existing == nil {
		existing = map[string]any{"match_all": map[string]any{}}
	}

	boolQuery := map[string]any{"filter": append([]any{existing}, includes...)}
	if len(excludes) > 0 {
		boolQuery["must_not"] = excludes
	}
	body["query"] = map[string]any{"bool": boolQuery}
	return nil
}

// FilterOpenSearch folds the given selections into an already-rendered query
// body, for a caller that assembled its own DSL rather than a stored profile —
// the connection browser.
//
// It exists so there is one clause compiler in the codebase: a second
// implementation would be a second set of rules for what excluding a value
// means, and the two would disagree on the day one of them was fixed.
//
// With no active filter it returns the body byte for byte, so turning filtering
// on cannot change what an unfiltered query sends.
func FilterOpenSearch(body string, filters []query.ColumnFilterValue) (string, error) {
	active := make([]query.ColumnFilterValue, 0, len(filters))
	for _, filter := range filters {
		if !filter.IsZero() {
			active = append(active, filter)
		}
	}
	if len(active) == 0 {
		return body, nil
	}
	// A blank body is the whole index, which is the one thing a filter can still
	// narrow — so it becomes the empty query rather than a decode error.
	decoded := map[string]any{}
	if strings.TrimSpace(body) != "" {
		var err error
		if decoded, err = decodeOpenSearchBody(body); err != nil {
			return "", err
		}
	}
	if err := applyOpenSearchFilters(decoded, active, nil); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return "", fmt.Errorf("encode filtered OpenSearch query: %w", err)
	}
	return string(encoded), nil
}

// dedupeStructuralParams drops the half of a selection the compiled query
// already carries. A list param bound into an esdsl condition emits its own
// terms clause, so re-applying the includes here would ask for them twice; the
// exclusions have no structural form and always ride the filter.
func dedupeStructuralParams(filters []query.ColumnFilterValue, paramUses []esdsl.ParamUse) ([]query.ColumnFilterValue, error) {
	usesByName := make(map[string][]esdsl.ParamUse, len(paramUses))
	for _, use := range paramUses {
		usesByName[use.Name] = append(usesByName[use.Name], use)
	}
	effective := make([]query.ColumnFilterValue, 0, len(filters))
	for _, filter := range filters {
		uses := usesByName[filter.Key]
		if len(uses) > 1 {
			return nil, fmt.Errorf("param %q has %d structural query mappings; list parameters require exactly one", filter.Key, len(uses))
		}
		if len(uses) == 1 {
			if uses[0].Field != filter.Field {
				return nil, fmt.Errorf("param %q maps native field %q but its query condition uses %q", filter.Key, filter.Field, uses[0].Field)
			}
			filter.Include = nil
		}
		if !filter.IsZero() {
			effective = append(effective, filter)
		}
	}
	return effective, nil
}

// openSearchFieldFilter accumulates every selection bound to one backend field,
// so two filters that name the same field intersect rather than compete.
type openSearchFieldFilter struct {
	field    string
	terms    []any
	exclude  []any
	match    []any
	notMatch []any
	rng      *query.FilterRange
	boolean  *bool
	// kind is the grammar the field was first filtered under, so a second
	// filter that reads it differently is refused rather than half-applied.
	kind query.ColumnFilterKind
}

// openSearchScope is the selections that share one compilation context: the
// whole document, or one entry of a `nested` field picked out by the constants
// in where.
//
// Two selections on the same nested path that pin different entries are separate
// scopes, because one entry cannot carry both — folding them together would ask
// for a tag whose key is at once "app" and "env" and match nothing.
type openSearchScope struct {
	nested string
	where  map[string]string
	order  []string
	fields map[string]*openSearchFieldFilter
}

// scopeKey identifies the compilation context a selection belongs to. The where
// constants are sorted into it so two selections that pin the same entry share a
// scope however their maps happened to be ordered.
func scopeKey(filter query.ColumnFilterValue) string {
	if filter.Nested == "" {
		return ""
	}
	pinned := make([]string, 0, len(filter.Where))
	for field, value := range filter.Where {
		pinned = append(pinned, field+"="+value)
	}
	sort.Strings(pinned)
	return filter.Nested + "\x00" + strings.Join(pinned, "\x00")
}

// openSearchFilterClauses folds the selections into bool clauses grouped by
// scope and then by field, in first-seen order so a body is byte-stable across
// runs. Every field contributes at most one filter clause and at most one
// must_not clause, whatever kind it was selected under.
func openSearchFilterClauses(filters []query.ColumnFilterValue) (includes, excludes []any, err error) {
	order := make([]string, 0, len(filters))
	scopes := make(map[string]*openSearchScope, len(filters))
	for _, filter := range filters {
		key := scopeKey(filter)
		scope, seen := scopes[key]
		if !seen {
			scope = &openSearchScope{
				nested: filter.Nested,
				where:  filter.Where,
				fields: make(map[string]*openSearchFieldFilter),
			}
			scopes[key] = scope
			order = append(order, key)
		}
		if err := scope.add(filter); err != nil {
			return nil, nil, err
		}
	}
	for _, key := range order {
		include, exclude, err := scopes[key].clauses()
		if err != nil {
			return nil, nil, err
		}
		includes = append(includes, include...)
		excludes = append(excludes, exclude...)
	}
	return includes, excludes, nil
}

func (s *openSearchScope) add(filter query.ColumnFilterValue) error {
	if s.nested != "" && !strings.HasPrefix(filter.Field, s.nested+".") {
		return fmt.Errorf("field %q is not inside nested %q", filter.Field, s.nested)
	}
	accumulated, seen := s.fields[filter.Field]
	if !seen {
		accumulated = &openSearchFieldFilter{field: filter.Field, kind: filter.Kind}
		s.fields[filter.Field] = accumulated
		s.order = append(s.order, filter.Field)
	}
	return accumulated.add(filter)
}

// clauses renders the scope, wrapping each clause in a nested query when it
// selects inside a repeated field.
//
// The wrapping is per clause rather than around the lot: a nested query asks
// whether *one* entry satisfies everything inside it, so two include clauses in
// one wrapper would demand a single entry carrying both, while two wrappers ask
// for an entry each — which is what two filters on one document mean everywhere
// else. Exclusions invert the whole wrapper, so "not app=legacy" reads as "no
// entry of this document is app=legacy" rather than "some entry is not".
func (s *openSearchScope) clauses() (includes, excludes []any, err error) {
	for _, field := range s.order {
		include, exclude, err := s.fields[field].clauses()
		if err != nil {
			return nil, nil, err
		}
		includes = append(includes, include...)
		excludes = append(excludes, exclude...)
	}
	if s.nested == "" {
		return includes, excludes, nil
	}
	pinned := s.pinnedClauses()
	wrap := func(clauses []any) []any {
		wrapped := make([]any, 0, len(clauses))
		for _, clause := range clauses {
			wrapped = append(wrapped, esdsl.NestedClause(s.nested, append(append([]any{}, pinned...), clause)))
		}
		return wrapped
	}
	return wrap(includes), wrap(excludes), nil
}

// pinnedClauses renders the constants that pick the entry, in field order so a
// body is byte-stable across runs.
func (s *openSearchScope) pinnedClauses() []any {
	fields := make([]string, 0, len(s.where))
	for field := range s.where {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	pinned := make([]any, 0, len(fields))
	for _, field := range fields {
		pinned = append(pinned, esdsl.TermClause(field, s.where[field]))
	}
	return pinned
}

func (f *openSearchFieldFilter) add(filter query.ColumnFilterValue) error {
	kind := filter.Kind
	if kind == "" {
		kind = query.ColumnFilterKindTerms
	}
	if existing := f.normalizedKind(); existing != kind {
		return fmt.Errorf("field %q is filtered as both %s and %s", f.field, existing, kind)
	}
	switch kind {
	case query.ColumnFilterKindTerms:
		f.terms = append(f.terms, toAnyValues(filter.Include)...)
		f.exclude = append(f.exclude, toAnyValues(filter.Exclude)...)
	case query.ColumnFilterKindText:
		f.match = append(f.match, toAnyValues(filter.Include)...)
		f.notMatch = append(f.notMatch, toAnyValues(filter.Exclude)...)
	case query.ColumnFilterKindRange, query.ColumnFilterKindTime:
		if err := f.narrow(filter.Range); err != nil {
			return err
		}
	case query.ColumnFilterKindBoolean:
		if f.boolean != nil && filter.Bool != nil && *f.boolean != *filter.Bool {
			return fmt.Errorf("field %q is filtered as both true and false", f.field)
		}
		f.boolean = filter.Bool
	default:
		return fmt.Errorf("field %q has no OpenSearch compiler for a %s filter", f.field, kind)
	}
	return nil
}

func (f *openSearchFieldFilter) normalizedKind() query.ColumnFilterKind {
	if f.kind == "" {
		return query.ColumnFilterKindTerms
	}
	return f.kind
}

// narrow intersects a second range on the same field. Two bounds on one side
// are a request that meant two things, and only one of them could have been
// applied, so it is refused rather than resolved by taking the last.
func (f *openSearchFieldFilter) narrow(next *query.FilterRange) error {
	if next == nil {
		return nil
	}
	if f.rng == nil {
		f.rng = &query.FilterRange{}
	}
	if next.Min != nil {
		if f.rng.Min != nil {
			return fmt.Errorf("field %q gets two lower bounds (%v and %v)", f.field, f.rng.Min.Value, next.Min.Value)
		}
		f.rng.Min = next.Min
	}
	if next.Max != nil {
		if f.rng.Max != nil {
			return fmt.Errorf("field %q gets two upper bounds (%v and %v)", f.field, f.rng.Max.Value, next.Max.Value)
		}
		f.rng.Max = next.Max
	}
	return nil
}

func (f *openSearchFieldFilter) clauses() (includes, excludes []any, err error) {
	if len(f.terms) > 0 {
		includes = append(includes, esdsl.TermsClause(f.field, f.terms))
	}
	if len(f.exclude) > 0 {
		excludes = append(excludes, esdsl.TermsClause(f.field, f.exclude))
	}
	for _, needle := range f.match {
		includes = append(includes, map[string]any{"match": map[string]any{f.field: needle}})
	}
	for _, needle := range f.notMatch {
		excludes = append(excludes, map[string]any{"match": map[string]any{f.field: needle}})
	}
	if f.rng != nil {
		bounds := esdsl.RangeBounds{}
		if edge := f.rng.Min; edge != nil {
			if edge.Inclusive {
				bounds.Gte = edge.Value
			} else {
				bounds.Gt = edge.Value
			}
		}
		if edge := f.rng.Max; edge != nil {
			if edge.Inclusive {
				bounds.Lte = edge.Value
			} else {
				bounds.Lt = edge.Value
			}
		}
		// No "format" is set: strict_date_optional_time is the default and
		// naming one would stop OpenSearch reading "now-15m" as date math.
		includes = append(includes, esdsl.RangeClause(f.field, bounds))
	}
	if f.boolean != nil {
		// A false selection is term:false, not must_not term:true — a document
		// missing the field is neither true nor false, and must_not would keep it.
		includes = append(includes, esdsl.TermClause(f.field, *f.boolean))
	}
	return includes, excludes, nil
}

func toAnyValues(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}
