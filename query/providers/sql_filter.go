package providers

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/flanksource/commons-db/query"
	"github.com/timberio/go-datemath"
)

// sqlBaseCTE is the name the author's statement is wrapped under. A collision
// is an error rather than a rename, because a query that silently referred to
// someone else's CTE would be a different query.
const (
	sqlBaseCTE = "__cdb_base"

	// sqlTotalColumn carries COUNT(*) OVER () on every paged row. It is stripped
	// before the row reaches a caller — it is the server's bookkeeping, not a
	// column anyone selected.
	sqlTotalColumn = "__cdb_total"
)

// FilterSQL wraps an operator-authored statement so the given selections narrow
// its result rather than its text, for a caller that runs its own SQL rather
// than a stored profile — the connection browser.
//
// It exists so there is one CTE wrapper and one predicate builder in the
// codebase: a second implementation would be a second set of quoting and
// binding rules to keep correct, and the two would disagree on the day one of
// them was fixed.
//
// driver is the connection type (models.ConnectionType*), not a registry key.
func FilterSQL(driver, statement string, filters []query.ColumnFilterValue) (string, []any, error) {
	return buildFilteredSQL(sqlDialect(driver), statement, filters)
}

// buildFilteredSQL wraps the author's statement so the filters apply to its
// result rather than to its text.
//
// The statement becomes a CTE and the predicate applies to the CTE's output
// columns, which is what makes an aliased expression filterable by the name the
// table already displays — filtering the underlying table would mean knowing
// which table an alias came from, which no amount of inference can answer.
//
// With no active filter it returns the statement and no args, byte for byte: an
// unfiltered profile runs exactly the statement it has always run, so turning
// filtering on cannot change what an untouched profile returns.
// buildPagedSQL builds the statement a page is read from: the author's query,
// filtered, plus — unless the caller waived it — the size of the whole filtered
// result on every row.
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
func buildPagedSQL(
	dialect sqlDialect,
	statement string,
	filters []query.ColumnFilterValue,
	order query.Order,
	page query.PageRequest,
) (string, []any, error) {
	where, args, err := filterClause(dialect, filters)
	if err != nil {
		return "", nil, err
	}
	total, err := dialect.quote(sqlTotalColumn)
	if err != nil {
		return "", nil, err
	}
	ordering, err := orderClause(dialect, order)
	if err != nil {
		return "", nil, err
	}
	wrapped, err := wrapAsBaseCTE(dialect, statement, func(base string) string {
		selection := fmt.Sprintf("SELECT %s.*, COUNT(*) OVER () AS %s FROM %s", base, total, base)
		if page.SkipTotal {
			selection = fmt.Sprintf("SELECT %s.* FROM %s", base, base)
		}
		clauses := []string{selection}
		if where != "" {
			clauses = append(clauses, "WHERE "+where)
		}
		if ordering != "" {
			clauses = append(clauses, "ORDER BY "+ordering)
		}
		// One past the ceiling, for the same reason a page reads one past its
		// limit: proving a further row exists is what separates a finished export
		// from one that stopped. Only an ordered statement takes it — SQL Server
		// refuses OFFSET/FETCH without an ORDER BY, and an unordered ceiling would
		// take an arbitrary N rows rather than the first N.
		if page.Ceiling > 0 && ordering != "" {
			clauses = append(clauses, dialect.limitTail(page.Ceiling+1))
		}
		return strings.Join(clauses, "\n")
	})
	if err != nil {
		return "", nil, err
	}
	return wrapped, args, nil
}

// orderClause renders a declared order for the wrapper. An undeclared order
// renders nothing, leaving whatever the author's statement does.
func orderClause(dialect sqlDialect, order query.Order) (string, error) {
	terms := make([]string, 0, len(order))
	for _, by := range order {
		column, err := dialect.quote(by.Column)
		if err != nil {
			return "", err
		}
		if by.Desc {
			column += " DESC"
		}
		terms = append(terms, column)
	}
	return strings.Join(terms, ", "), nil
}

// filterClause renders the active selections as one WHERE body, or "" when
// nothing is selected.
func filterClause(dialect sqlDialect, filters []query.ColumnFilterValue) (string, []any, error) {
	active := make([]query.ColumnFilterValue, 0, len(filters))
	for _, filter := range filters {
		if !filter.IsZero() {
			active = append(active, filter)
		}
	}
	if len(active) == 0 {
		return "", nil, nil
	}
	predicate, err := sqlPredicates(dialect, active)
	if err != nil {
		return "", nil, err
	}
	return renderClause(dialect, predicate)
}

func buildFilteredSQL(dialect sqlDialect, statement string, filters []query.ColumnFilterValue) (string, []any, error) {
	active := make([]query.ColumnFilterValue, 0, len(filters))
	for _, filter := range filters {
		if !filter.IsZero() {
			active = append(active, filter)
		}
	}
	if len(active) == 0 {
		return statement, nil, nil
	}
	predicate, err := sqlPredicates(dialect, active)
	if err != nil {
		return "", nil, err
	}
	where, args, err := renderClause(dialect, predicate)
	if err != nil {
		return "", nil, err
	}
	wrapped, err := wrapAsBaseCTE(dialect, statement, func(base string) string {
		return fmt.Sprintf("SELECT * FROM %s WHERE %s", base, where)
	})
	if err != nil {
		return "", nil, err
	}
	return wrapped, args, nil
}

// wrapAsBaseCTE builds the WITH form and hands tail the quoted CTE name to
// select from, hoisting any CTEs the author already wrote into the same list
// rather than nesting them.
//
// Nesting is not an option: SQL Server rejects a WITH inside a CTE body
// outright. Hoisting is legal in every dialect, so there is one shape to build
// and one shape to test.
func wrapAsBaseCTE(dialect sqlDialect, statement string, tail func(base string) string) (string, error) {
	if err := assertNoPlaceholders(dialect, statement); err != nil {
		return "", err
	}
	prefix, body, cteNames, err := splitWithPrefix(statement)
	if err != nil {
		return "", err
	}
	for _, name := range cteNames {
		if strings.EqualFold(name, sqlBaseCTE) {
			return "", fmt.Errorf(
				"the query already defines a CTE named %q, which is the name column filters wrap it in; rename it", sqlBaseCTE)
		}
	}
	base, err := dialect.quote(sqlBaseCTE)
	if err != nil {
		return "", err
	}
	head := "WITH "
	if prefix != "" {
		head = strings.TrimSuffix(prefix, ",") + ", "
	}
	return fmt.Sprintf("%s%s AS (\n%s\n)\n%s", head, base, body, tail(base)), nil
}

// sqlPredicates turns the resolved selections into one conjunction.
//
// The values one field collects are alternatives, so they become a single IN —
// one equality per value would AND them and match nothing. Distinct fields stay
// ANDed, and two filters bound to the same backend field (a column filter and a
// list param, say) merge into one clause.
func sqlPredicates(dialect sqlDialect, filters []query.ColumnFilterValue) (squirrel.Sqlizer, error) {
	order := make([]string, 0, len(filters))
	byField := make(map[string]*query.ColumnFilterValue, len(filters))
	for _, filter := range filters {
		merged, seen := byField[filter.Field]
		if !seen {
			copied := filter
			byField[filter.Field] = &copied
			order = append(order, filter.Field)
			continue
		}
		if merged.Kind.Normalized() != filter.Kind.Normalized() {
			return nil, fmt.Errorf("field %q is filtered as both %s and %s",
				filter.Field, merged.Kind.Normalized(), filter.Kind.Normalized())
		}
		merged.Include = append(merged.Include, filter.Include...)
		merged.Exclude = append(merged.Exclude, filter.Exclude...)
		if err := mergeRange(merged, filter); err != nil {
			return nil, err
		}
	}

	conditions := squirrel.And{}
	for _, field := range order {
		clause, err := sqlFieldPredicate(dialect, *byField[field])
		if err != nil {
			return nil, err
		}
		if clause != nil {
			conditions = append(conditions, clause)
		}
	}
	if len(conditions) == 0 {
		return nil, fmt.Errorf("no filter produced a predicate")
	}
	return unwrapSingle(conditions), nil
}

// unwrapSingle drops a conjunction of one, so a single filter renders as its own
// predicate rather than nested in parentheses that say nothing.
func unwrapSingle(conditions squirrel.And) squirrel.Sqlizer {
	if len(conditions) == 1 {
		return conditions[0]
	}
	return conditions
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

// sqlFieldPredicate is the clause one backend field contributes.
func sqlFieldPredicate(dialect sqlDialect, filter query.ColumnFilterValue) (squirrel.Sqlizer, error) {
	column, err := dialect.quote(filter.Field)
	if err != nil {
		return nil, err
	}
	conditions := squirrel.And{}
	switch kind := filter.Kind.Normalized(); kind {
	case query.ColumnFilterKindTerms, query.ColumnFilterKindBoolean:
		args, err := sqlFilterArgs(kind, filter)
		if err != nil {
			return nil, err
		}
		if len(args.include) > 0 {
			conditions = append(conditions, squirrel.Eq{column: args.include})
		}
		if len(args.exclude) > 0 {
			// A row with no value was not one of the excluded values, so the
			// exclusion must keep it — which is also what OpenSearch's
			// must_not:terms does, and the two backends must not disagree about
			// what excluding a value means.
			conditions = append(conditions, squirrel.Or{
				squirrel.Eq{column: nil},
				squirrel.NotEq{column: args.exclude},
			})
		}
	case query.ColumnFilterKindText:
		for _, needle := range filter.Include {
			predicate, pattern := dialect.likeMatch(column, needle)
			conditions = append(conditions, squirrel.Expr(predicate, pattern))
		}
		for _, needle := range filter.Exclude {
			predicate, pattern := dialect.likeMatch(column, needle)
			conditions = append(conditions, squirrel.Or{
				squirrel.Eq{column: nil},
				squirrel.Expr("NOT ("+predicate+")", pattern),
			})
		}
	case query.ColumnFilterKindRange, query.ColumnFilterKindTime:
		if filter.Range == nil {
			return nil, nil
		}
		// The comparisons are squirrel's typed clauses rather than a hand-built
		// "col op ?" string, so the column never has to be concatenated into a
		// SQL fragment to be compared against a bound value.
		for _, edge := range []struct {
			bound  *query.FilterBound
			closed func(any) squirrel.Sqlizer
			open   func(any) squirrel.Sqlizer
		}{
			{
				bound:  filter.Range.Min,
				closed: func(v any) squirrel.Sqlizer { return squirrel.GtOrEq{column: v} },
				open:   func(v any) squirrel.Sqlizer { return squirrel.Gt{column: v} },
			},
			{
				bound:  filter.Range.Max,
				closed: func(v any) squirrel.Sqlizer { return squirrel.LtOrEq{column: v} },
				open:   func(v any) squirrel.Sqlizer { return squirrel.Lt{column: v} },
			},
		} {
			if edge.bound == nil {
				continue
			}
			value, err := sqlBoundValue(kind, edge.bound.Value)
			if err != nil {
				return nil, err
			}
			compare := edge.open
			if edge.bound.Inclusive {
				compare = edge.closed
			}
			conditions = append(conditions, compare(value))
		}
	default:
		return nil, fmt.Errorf("field %q has no SQL compiler for a %s filter", filter.Field, kind)
	}
	if len(conditions) == 0 {
		return nil, nil
	}
	return unwrapSingle(conditions), nil
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
func sqlBoundValue(kind query.ColumnFilterKind, value any) (any, error) {
	if kind != query.ColumnFilterKindTime {
		return value, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("time bound %v is not a string", value)
	}
	expression, err := datemath.Parse(text)
	if err != nil {
		return nil, fmt.Errorf("%q is not an RFC3339 time or date math: %w", text, err)
	}
	return expression.Time(datemath.WithNow(time.Now().UTC())), nil
}

// renderClause formats one predicate for dialect.
//
// Only the predicate is ever rendered. A PlaceholderFormat rewrites every "?"
// in whatever string it is handed with no idea which are inside a literal, so
// handing it the author's statement would silently renumber a jsonb "?"
// operator into a bind marker. The author's statement sits untouched inside the
// CTE and this function never sees it.
func renderClause(dialect sqlDialect, clause squirrel.Sqlizer) (string, []any, error) {
	rendered, args, err := clause.ToSql()
	if err != nil {
		return "", nil, fmt.Errorf("failed to build column filter predicate: %w", err)
	}
	rendered, err = dialect.placeholders().ReplacePlaceholders(rendered)
	if err != nil {
		return "", nil, fmt.Errorf("failed to bind column filter values: %w", err)
	}
	return rendered, args, nil
}
