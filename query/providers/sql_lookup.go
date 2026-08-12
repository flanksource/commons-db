package providers

import (
	"fmt"
	"strings"

	"github.com/Masterminds/squirrel"
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// LookupFilterValues answers "what can this column be filtered to" from the
// same wrapped base the rows come from, with every other active selection
// applied — so the options offered are the options the table can actually show.
//
// query.LookupFilterValues has already dropped the filter being looked up from
// req.Filters, so what arrives here is exactly the sibling set.
func (p sqlProvider) LookupFilterValues(
	ctx context.Context,
	req query.ProviderRequest,
	binding query.ColumnFilterBinding,
	search string,
	limit int,
) ([]query.FilterOption, *query.Total, error) {
	if req.Query == "" {
		return nil, nil, fmt.Errorf("sql query is required")
	}
	if limit <= 0 {
		return nil, nil, fmt.Errorf("filter value lookup needs a positive limit")
	}
	opts, err := query.DecodeOptions[sqlOptions](req.Options)
	if err != nil {
		return nil, nil, err
	}
	client, dialect, release, err := sqlConnect(ctx, sqlConnectRequest{
		Connection: req.Connection, ConnType: p.connType, Options: opts,
	})
	if err != nil {
		return nil, nil, err
	}
	defer release()
	defer client.Close()

	statement, args, err := buildLookupSQL(dialect, req.Query, binding, req.Filters, search, limit)
	if err != nil {
		return nil, nil, err
	}

	// statement wraps the author's query in a CTE built from constant syntax;
	// the looked-up column is validated and quoted, and the search term is bound.
	// codeql[go/sql-injection]
	rows, err := client.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to look up filter values: %w", err)
	}
	defer rows.Close()

	options := make([]query.FilterOption, 0, limit)
	total := 0
	for rows.Next() {
		var value any
		var count, distinct int64
		if err := rows.Scan(&value, &count, &distinct); err != nil {
			return nil, nil, fmt.Errorf("failed to read filter values: %w", err)
		}
		options = append(options, query.FilterOption{Value: fmt.Sprintf("%v", value), Count: count})
		total = int(distinct)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("failed to read filter values: %w", err)
	}
	// COUNT(*) OVER () over the grouped set is the number of distinct values,
	// not an estimate of it.
	return options, &query.Total{Value: int64(total), Exact: true}, nil
}

// buildLookupSQL builds the distinct-values query over the wrapped base.
//
// The distinct count comes from COUNT(*) OVER () on the grouped set rather than
// a second statement or a limit+1 probe. A second statement would run the
// author's query twice at two different snapshots, and a probe can only ever
// say "and 1 more" when the truth might be "and 4,000 more" — a number a reader
// would take at face value.
func buildLookupSQL(
	dialect sqlDialect,
	statement string,
	binding query.ColumnFilterBinding,
	siblings []query.ColumnFilterValue,
	search string,
	limit int,
) (string, []any, error) {
	if !binding.Kind.Lookupable() {
		return "", nil, fmt.Errorf(
			"filter %q is a %s filter and has no values to list", binding.Key, binding.Kind.Normalized())
	}
	column, err := dialect.quote(binding.Field)
	if err != nil {
		return "", nil, err
	}

	conditions := squirrel.And{}
	active := make([]query.ColumnFilterValue, 0, len(siblings))
	for _, sibling := range siblings {
		if !sibling.IsZero() {
			active = append(active, sibling)
		}
	}
	if len(active) > 0 {
		predicate, err := sqlPredicates(dialect, active)
		if err != nil {
			return "", nil, err
		}
		conditions = append(conditions, predicate)
	}
	// NULL is not a value anyone can select, so it is not offered as one.
	conditions = append(conditions, squirrel.Expr(column+" IS NOT NULL"))
	if search != "" {
		predicate, pattern := dialect.likeMatch(column, search)
		conditions = append(conditions, squirrel.Expr(predicate, pattern))
	}

	where, args, err := renderClause(dialect, unwrapSingle(conditions))
	if err != nil {
		return "", nil, err
	}

	wrapped, err := wrapAsBaseCTE(dialect, statement, func(base string) string {
		return strings.Join([]string{
			fmt.Sprintf("SELECT %s AS value, COUNT(*) AS count, COUNT(*) OVER () AS total", column),
			fmt.Sprintf("FROM %s", base),
			fmt.Sprintf("WHERE %s", where),
			fmt.Sprintf("GROUP BY %s", column),
			"ORDER BY 2 DESC, 1 ASC",
			dialect.limitTail(limit),
		}, "\n")
	})
	if err != nil {
		return "", nil, err
	}
	return wrapped, args, nil
}
