package sqlite

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/sqlglot-go/sqlglot"
	"github.com/timberio/go-datemath"
)

const (
	// sqlTotalColumn carries COUNT(*) OVER () on every paged row. It is stripped
	// before the row reaches a caller — it is the server's bookkeeping, not a
	// column anyone selected.
	sqlTotalColumn = "__cdb_total"

	// sqlPageAlias names the counted page a total-bearing read selects from.
	sqlPageAlias = "__cdb_page"
)

// buildPagedSQL builds the statement a page is read from: the author's query,
// filtered, plus — unless the caller waived it — the size of the whole filtered
// result on every row.
//
// The author's statement becomes a CTE and the predicate applies to the CTE's
// output columns, which is what makes an aliased expression filterable by the
// name the table already displays — filtering the underlying table would mean
// knowing which table an alias came from, which no amount of inference can
// answer.
//
// The count comes from COUNT(*) OVER () on the wrapped statement rather than a
// second SELECT COUNT(*). A second statement would run the author's query twice
// at two different snapshots, so the count could disagree with the rows it is
// meant to describe — the same reasoning the filter-value lookup already
// follows.
//
// That window aggregate is also why page.SkipTotal exists. COUNT(*) OVER () with
// no PARTITION BY is a whole-partition aggregate: the database materializes the
// entire filtered result before emitting the first row, so time-to-first-byte is
// full-scan time and no amount of client-side ceiling stops the server. A table
// pays that to say what it is a page of. An export is taking everything and has
// a trailer for the one fact it still needs, so it waives the total and gets a
// walk that streams — and pushes its ceiling down so the backend stops where it
// does.
//
// The order is restated on the wrapper. Selecting out of a CTE does not
// guarantee the CTE's own ORDER BY survives, and an order that silently stops
// applying is exactly the instability paging cannot tolerate.
func buildPagedSQL(dialect sqlDialect, request SQLPageRequest) (*sqlStatement, error) {
	statement, err := newSQLStatement(dialect)
	if err != nil {
		return nil, err
	}
	base, err := statement.parseBase(request.sqlQuery())
	if err != nil {
		return nil, err
	}
	where, err := statement.filterPredicate(request.Filters)
	if err != nil {
		return nil, err
	}
	resume, err := statement.keysetPredicate(request.Order, request.Position)
	if err != nil {
		return nil, err
	}
	terms, err := orderTerms(dialect, request.Order)
	if err != nil {
		return nil, err
	}
	if len(terms) == 0 && dialect == dialectSQLServer {
		terms = sqlNodes(arbitraryOrder())
	}
	page := pagedSelect(dialect, where, resume, request.Page.SkipTotal)
	if len(terms) > 0 {
		page.Set("order", sqlglot.New("Order", kv("expressions", terms)))
	}
	fetch := request.Page.Limit + 1
	if request.Page.Ceiling > 0 && request.Page.Ceiling < request.Page.Limit {
		fetch = request.Page.Ceiling + 1
	}
	setPageTail(dialect, page, fetch, request.Page.Offset)
	if err := wrapAsBaseCTE(base, page); err != nil {
		return nil, err
	}
	statement.tree = page
	return statement, nil
}

// pagedSelect selects the filtered base, resumed past the cursor. With a total
// wanted, the filtered rows are counted first and the cursor applies to the
// counted set, so the total describes the whole result rather than what is
// left of it.
func pagedSelect(dialect sqlDialect, where, resume sqlNode, skipTotal bool) sqlNode {
	if skipTotal {
		narrowed := where
		if where != nil && resume != nil {
			narrowed = binary("And", where, resume)
		} else if resume != nil {
			narrowed = resume
		}
		return sqlglot.New("Select", kv("expressions", sqlNodes(allColumnsOf(sqlBaseCTE))),
			kv("from_", fromItem(baseTable())), kv("where", whereOf(narrowed)))
	}
	counted := sqlglot.New("Select",
		kv("expressions", sqlNodes(allColumnsOf(sqlBaseCTE), aliased(totalOver(dialect), identifier(sqlTotalColumn)))),
		kv("from_", fromItem(baseTable())), kv("where", whereOf(where)))
	return sqlglot.New("Select", kv("expressions", sqlNodes(allColumnsOf(sqlPageAlias))),
		kv("from_", fromItem(subqueryAs(counted, sqlPageAlias))), kv("where", whereOf(resume)))
}

func (r SQLPageRequest) sqlQuery() sqlQuery {
	return sqlQuery{Text: r.Query, Args: r.QueryArgs, Identifiers: r.QueryIdentifiers}
}

// keysetPredicate resumes strictly after position under orderTerms' ordering,
// where nulls sort last in both directions. Alternative i holds the columns
// before i equal to their keys (a null key as IS NULL) and column i strictly
// after its key: past a value, a later value or a null; past a null, nothing,
// so that alternative is dropped.
func (s *sqlStatement) keysetPredicate(order query.Order, position query.CursorPosition) (sqlNode, error) {
	if position.IsZero() {
		return nil, nil
	}
	if err := order.ValidatePosition(position.Keys); err != nil {
		return nil, err
	}
	columns := make([]sqlNode, len(order))
	for index, by := range order {
		column, err := fieldColumn(by.Column)
		if err != nil {
			return nil, err
		}
		columns[index] = column
	}
	alternatives := []sqlNode{}
	for index, by := range order {
		key := position.Keys[index]
		if key == nil {
			continue
		}
		conditions := []sqlNode{}
		for prefix := 0; prefix < index; prefix++ {
			conditions = append(conditions, s.equalTo(columns[prefix], position.Keys[prefix]))
		}
		after := "GT"
		if by.Desc {
			after = "LT"
		}
		conditions = append(conditions, conjunction("Or", sqlNodes(
			binary(after, columns[index].Copy(), s.bind(key)), isNull(columns[index]))))
		alternatives = append(alternatives, conjunction("And", conditions))
	}
	return conjunction("Or", alternatives), nil
}

// equalTo is column = key, or column IS NULL for a null key.
func (s *sqlStatement) equalTo(column sqlNode, key any) sqlNode {
	if key == nil {
		return isNull(column)
	}
	return binary("EQ", column.Copy(), s.bind(key))
}

// orderTerms renders a declared order for the wrapper, nulls last in both
// directions. An undeclared order renders nothing, leaving whatever the
// author's statement does.
//
// SQL Server and MySQL have no NULLS LAST, so a leading IS NULL term puts the
// nulls after every value there; the other dialects state it, and the
// generator writes NULLS LAST wherever it is not already their default
// (SQLite puts nulls first ascending, postgres first descending).
func orderTerms(dialect sqlDialect, order query.Order) ([]sqlNode, error) {
	terms := make([]sqlNode, 0, len(order))
	for _, by := range order {
		column, err := fieldColumn(by.Column)
		if err != nil {
			return nil, err
		}
		switch dialect {
		case dialectSQLServer, dialectMySQL:
			terms = append(terms, nullsLastKey(column), ordered(column, by.Desc, defaultNullsFirst(dialect, by.Desc)))
		default:
			terms = append(terms, ordered(column, by.Desc, false))
		}
	}
	return terms, nil
}

// filterPredicate is the active selections as one predicate, or nil when
// nothing is selected.
func (s *sqlStatement) filterPredicate(filters []query.ColumnFilterValue) (sqlNode, error) {
	active := make([]query.ColumnFilterValue, 0, len(filters))
	for _, filter := range filters {
		if !filter.IsZero() {
			active = append(active, filter)
		}
	}
	if len(active) == 0 {
		return nil, nil
	}
	return s.sqlPredicates(active)
}

// sqlPredicates turns the resolved selections into one conjunction.
//
// The values one field collects are alternatives, so they become a single IN —
// one equality per value would AND them and match nothing. Distinct fields stay
// ANDed, and two filters bound to the same backend field (a column filter and a
// list param, say) merge into one clause.
func (s *sqlStatement) sqlPredicates(filters []query.ColumnFilterValue) (sqlNode, error) {
	order, byField, err := mergeFieldFilters(filters)
	if err != nil {
		return nil, err
	}
	conditions := []sqlNode{}
	for _, field := range order {
		clause, err := s.sqlFieldPredicate(*byField[field])
		if err != nil {
			return nil, err
		}
		conditions = append(conditions, clause)
	}
	predicate := conjunction("And", conditions)
	if predicate == nil {
		return nil, fmt.Errorf("no filter produced a predicate")
	}
	return predicate, nil
}

// mergeFieldFilters groups filters by backend field, in first-seen order.
func mergeFieldFilters(filters []query.ColumnFilterValue) ([]string, map[string]*query.ColumnFilterValue, error) {
	order := make([]string, 0, len(filters))
	byField := make(map[string]*query.ColumnFilterValue, len(filters))
	for _, filter := range filters {
		merged, seen := byField[filter.Field]
		if !seen {
			// The merge below writes into the copy, so it must own everything it
			// writes: the caller's filters are what a cursor is fingerprinted
			// from, and a range edge merged into them would stale it.
			copied := filter
			copied.Include, copied.Exclude = slices.Clone(filter.Include), slices.Clone(filter.Exclude)
			if filter.Range != nil {
				span := *filter.Range
				copied.Range = &span
			}
			byField[filter.Field] = &copied
			order = append(order, filter.Field)
			continue
		}
		// Compatibility is by compiled clause, not by declared kind: a list param
		// bound to the field an identifier column filters is still one IN. The
		// message keeps the declared kinds, so an operator reads back the words
		// from their own profile.
		if merged.Kind.CompilesAs() != filter.Kind.CompilesAs() {
			return nil, nil, fmt.Errorf("field %q is filtered as both %s and %s",
				filter.Field, merged.Kind.Normalized(), filter.Kind.Normalized())
		}
		if merged.Array != filter.Array {
			return nil, nil, fmt.Errorf("field %q is filtered both as an array and as a value", filter.Field)
		}
		merged.Include = append(merged.Include, filter.Include...)
		merged.Exclude = append(merged.Exclude, filter.Exclude...)
		if err := mergeRange(merged, filter); err != nil {
			return nil, nil, err
		}
	}
	return order, byField, nil
}

func mergeRange(into *query.ColumnFilterValue, next query.ColumnFilterValue) error {
	if next.Range == nil {
		return nil
	}
	if into.Range == nil {
		into.Range = &query.FilterRange{}
	}
	if next.Range.Min != nil {
		if into.Range.Min != nil {
			return fmt.Errorf("field %q gets two lower bounds (%v and %v)", into.Field, into.Range.Min.Value, next.Range.Min.Value)
		}
		into.Range.Min = next.Range.Min
	}
	if next.Range.Max != nil {
		if into.Range.Max != nil {
			return fmt.Errorf("field %q gets two upper bounds (%v and %v)", into.Field, into.Range.Max.Value, next.Range.Max.Value)
		}
		into.Range.Max = next.Range.Max
	}
	return nil
}

// sqlFieldPredicate is the clause one backend field contributes, or nil.
func (s *sqlStatement) sqlFieldPredicate(filter query.ColumnFilterValue) (sqlNode, error) {
	// A SQL result is rows of columns; there is no entry inside a row to scope a
	// selection to. Dropping the container silently would widen the selection to
	// the whole row, which is the wrong rows rather than fewer of them.
	if filter.Nested != "" {
		return nil, fmt.Errorf(
			"field %q is scoped to nested %q, which SQL has no equivalent of", filter.Field, filter.Nested)
	}
	column, err := fieldColumn(filter.Field)
	if err != nil {
		return nil, err
	}
	if filter.Array {
		return s.sqlArrayPredicate(column, filter)
	}
	// Every kind compiles through the clause its family compiles through: an
	// exact match as terms, a duration as a range, a date as a time. See
	// ColumnFilterKind.CompilesAs.
	switch kind := filter.Kind.CompilesAs(); kind {
	case query.ColumnFilterKindTerms, query.ColumnFilterKindBoolean:
		return s.termsPredicate(column, kind, filter)
	case query.ColumnFilterKindText:
		conditions := []sqlNode{}
		for _, needle := range filter.Include {
			conditions = append(conditions, s.likeMatch(column, needle))
		}
		for _, needle := range filter.Exclude {
			conditions = append(conditions, excluding(column, s.likeMatch(column, needle)))
		}
		return conjunction("And", conditions), nil
	case query.ColumnFilterKindMatch:
		return s.matchPredicate(column, filter), nil
	case query.ColumnFilterKindRange, query.ColumnFilterKindTime:
		return s.rangePredicate(column, kind, filter.Range)
	default:
		return nil, fmt.Errorf("field %q has no SQL compiler for a %s filter", filter.Field, filter.Kind.Normalized())
	}
}

// excluding keeps a row the predicate does not match. A row with no value was
// not one of the excluded values, so the exclusion must keep it — which is also
// what OpenSearch's must_not:terms does, and the two backends must not
// disagree about what excluding a value means.
func excluding(column, predicate sqlNode) sqlNode {
	return conjunction("Or", sqlNodes(isNull(column), negate(paren(predicate))))
}

func (s *sqlStatement) termsPredicate(column sqlNode, kind query.ColumnFilterKind, filter query.ColumnFilterValue) (sqlNode, error) {
	values, err := sqlFilterArgs(kind, filter)
	if err != nil {
		return nil, err
	}
	conditions := []sqlNode{}
	if len(values.include) > 0 {
		conditions = append(conditions, inList(column.Copy(), s.bindAll(values.include)))
	}
	if len(values.exclude) > 0 {
		conditions = append(conditions, conjunction("Or", sqlNodes(
			isNull(column), negate(inList(column.Copy(), s.bindAll(values.exclude))))))
	}
	return conjunction("And", conditions), nil
}

// matchPredicate is collections.MatchItem: a row matching any exclusion is
// out, then one matching any inclusion is in; with exclusions alone, everything
// they leave is in. A row with no value was not one of the excluded values, as
// for terms.
func (s *sqlStatement) matchPredicate(column sqlNode, filter query.ColumnFilterValue) sqlNode {
	included := []sqlNode{}
	for _, pattern := range filter.Include {
		included = append(included, s.globMatch(column, query.ParseMatchPattern(pattern)))
	}
	conditions := sqlNodes(conjunction("Or", included))
	for _, pattern := range filter.Exclude {
		conditions = append(conditions, excluding(column, s.globMatch(column, query.ParseMatchPattern(pattern))))
	}
	return conjunction("And", conditions)
}

func (s *sqlStatement) rangePredicate(column sqlNode, kind query.ColumnFilterKind, span *query.FilterRange) (sqlNode, error) {
	if span == nil {
		return nil, nil
	}
	conditions := []sqlNode{}
	for _, edge := range []struct {
		bound        *query.FilterBound
		closed, open string
	}{{span.Min, "GTE", "GT"}, {span.Max, "LTE", "LT"}} {
		if edge.bound == nil {
			continue
		}
		value, err := sqlBoundValue(s.dialect, kind, edge.bound.Value)
		if err != nil {
			return nil, err
		}
		compare := edge.open
		if edge.bound.Inclusive {
			compare = edge.closed
		}
		conditions = append(conditions, binary(compare, column.Copy(), s.bind(value)))
	}
	return conjunction("And", conditions), nil
}

func (s *sqlStatement) bindAll(values []any) []sqlNode {
	markers := make([]sqlNode, len(values))
	for index, value := range values {
		markers[index] = s.bind(value)
	}
	return markers
}

// sqlFilterValues are the bound operands one field collected, in the Go types
// its kind compares as.
type sqlFilterValues struct {
	include []any
	exclude []any
}

// sqlFilterArgs coerces the wire strings one field collected into the values it
// binds as, so a yes/no filter binds a real bool and a bad one fails here
// rather than as a backend type error three layers down.
func sqlFilterArgs(kind query.ColumnFilterKind, filter query.ColumnFilterValue) (sqlFilterValues, error) {
	values := sqlFilterValues{}
	for _, side := range []struct {
		from []string
		into *[]any
	}{{filter.Include, &values.include}, {filter.Exclude, &values.exclude}} {
		for _, raw := range side.from {
			if kind != query.ColumnFilterKindBoolean {
				*side.into = append(*side.into, raw)
				continue
			}
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				return sqlFilterValues{}, fmt.Errorf("field %q: %q is not true or false", filter.Field, raw)
			}
			*side.into = append(*side.into, parsed)
		}
	}
	if kind == query.ColumnFilterKindBoolean && filter.Bool != nil {
		values.include = append(values.include, *filter.Bool)
	}
	return values, nil
}

// sqlBoundValue resolves one range edge to the value it binds as. SQL has no
// server-side date math, so "now-1h" is resolved here — unlike OpenSearch,
// which resolves it itself and must receive it unchanged.
//
// The kind arrives already mapped through CompilesAs, so a date bound is a time
// bound here and gets the same resolution, and a duration bound is a numeric
// one that the parser already reduced to the column's unit.
//
// SQLite has no time type: its instants are TEXT, so a sqlite bound is bound
// as text in the one layout they are stored in (sqlitetable.TimeLayout). Every
// other dialect's driver binds a time.Time as a time.
func sqlBoundValue(dialect sqlDialect, kind query.ColumnFilterKind, value any) (any, error) {
	if kind != query.ColumnFilterKindTime {
		return value, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("time bound %v is not a string", value)
	}
	// An RFC3339 instant is read as one before date math is tried: date math
	// reads no more than three fractional digits, and a resolved datetime param
	// renders nine.
	resolved, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		expression, mathErr := datemath.Parse(text)
		if mathErr != nil {
			return nil, fmt.Errorf("%q is not an RFC3339 time or date math: %w", text, mathErr)
		}
		resolved = expression.Time(datemath.WithNow(time.Now().UTC()))
	}
	if dialect == dialectSQLite {
		return sqlitetable.FormatTime(resolved), nil
	}
	return resolved, nil
}
