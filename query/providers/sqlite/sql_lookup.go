package sqlite

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/sqlglot-go/sqlglot"
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
	if limit > query.MaxFilterLookupLimit {
		return nil, nil, fmt.Errorf("filter value lookup limit must be at most %d, got %d", query.MaxFilterLookupLimit, limit)
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
	defer func() { _ = client.Close() }()
	if err := requireSQLArrayFilters(ctx, client, dialect, binding.Array || filtersUseArrays(req.Filters)); err != nil {
		return nil, nil, err
	}

	built, err := buildLookupSQL(dialect, sqlLookupRequest{
		Query:    sqlQuery{Text: req.Query, Args: req.QueryArgs, Identifiers: req.QueryIdentifiers},
		Binding:  binding,
		Siblings: req.Filters,
		Search:   search,
		Limit:    limit,
	})
	if err != nil {
		return nil, nil, err
	}
	statement, args, err := built.render()
	if err != nil {
		return nil, nil, err
	}
	started := time.Now()
	req.Diagnostics.RecordRequest(statement, args, map[string]any{
		"dialect": string(dialect), "operation": "filter-values",
	})

	options, total, err := readFilterValues(ctx, client, statement, driverArgs(dialect, args), limit)
	publishSQLStatement(req.Connection, dialect, statement, args, started, len(options), err)
	if err != nil {
		return nil, nil, err
	}
	req.Diagnostics.RecordResponse(started, len(options), map[string]any{
		"distinctValues": total,
	})
	// COUNT(*) OVER () over the grouped set is the number of distinct values,
	// not an estimate of it.
	return options, &query.Total{Value: int64(total), Exact: true}, nil
}

// readFilterValues runs a lookup statement and reads up to limit options, and
// the number of distinct values there are.
func readFilterValues(ctx context.Context, client *sql.DB, statement string, args []any, limit int) ([]query.FilterOption, int, error) {
	rows, err := client.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to look up filter values: %w", err)
	}
	defer func() { _ = rows.Close() }()

	options := make([]query.FilterOption, 0, limit)
	total := 0
	for rows.Next() {
		var value any
		var count, distinct int64
		if err := rows.Scan(&value, &count, &distinct); err != nil {
			return nil, 0, fmt.Errorf("failed to read filter values: %w", err)
		}
		options = append(options, query.FilterOption{Value: fmt.Sprintf("%v", value), Count: count})
		total = int(distinct)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("failed to read filter values: %w", err)
	}
	return options, total, nil
}

// buildLookupSQL builds the distinct-values query over the wrapped base.
//
// The distinct count comes from COUNT(*) OVER () on the grouped set rather than
// a second statement or a limit+1 probe. A second statement would run the
// author's query twice at two different snapshots, and a probe can only ever
// say "and 1 more" when the truth might be "and 4,000 more" — a number a reader
// would take at face value.
func buildLookupSQL(dialect sqlDialect, request sqlLookupRequest) (*sqlStatement, error) {
	if !request.Binding.Kind.Lookupable() {
		return nil, fmt.Errorf("filter %q is a %s filter and has no values to list",
			request.Binding.Key, request.Binding.Kind.Normalized())
	}
	column, err := fieldColumn(request.Binding.Field)
	if err != nil {
		return nil, err
	}
	statement, err := newSQLStatement(dialect)
	if err != nil {
		return nil, err
	}
	base, err := statement.parseBase(request.Query)
	if err != nil {
		return nil, err
	}
	scope, err := statement.filterPredicate(request.Siblings)
	if err != nil {
		return nil, err
	}
	selection, value, member := scalarLookupSelect(dialect, column), column, sqlNode(nil)
	if request.Binding.Array {
		if selection, value, member, err = arrayLookupSelect(dialect, column); err != nil {
			return nil, fmt.Errorf("field %q is an array: %w", request.Binding.Field, err)
		}
		// The siblings scope the rows before they are numbered and expanded.
		rows, _ := selection.Args["from_"].(sqlNode)
		rows.This().This().Set("where", whereOf(scope))
		scope = nil
	}
	// NULL is not a value anyone can select, so it is not offered as one.
	conditions := sqlNodes(scope, member, isNotNull(value))
	if request.Search != "" {
		conditions = append(conditions, statement.likeMatch(value, request.Search))
	}
	selection.Set("where", whereOf(conjunction("And", conditions)))
	selection.Set("order", sqlglot.New("Order", kv("expressions", sqlNodes(
		ordered(number(2), true, defaultNullsFirst(dialect, true)),
		ordered(number(1), false, defaultNullsFirst(dialect, false))))))
	setPageTail(dialect, selection, request.Limit, 0)
	if err := wrapAsBaseCTE(base, selection); err != nil {
		return nil, err
	}
	statement.tree = selection
	return statement, nil
}

// sqlLookupRequest is what a distinct-values lookup is built from: the
// author's query, the filter being looked up, every other active selection,
// and the typed search.
type sqlLookupRequest struct {
	Query    sqlQuery
	Binding  query.ColumnFilterBinding
	Siblings []query.ColumnFilterValue
	Search   string
	Limit    int
}

// scalarLookupSelect counts each value of column over the wrapped base.
func scalarLookupSelect(dialect sqlDialect, column sqlNode) sqlNode {
	return sqlglot.New("Select",
		kv("expressions", sqlNodes(
			aliased(column.Copy(), bareIdentifier("value")),
			aliased(countOf(dialect, sqlglot.New("Star")), bareIdentifier("count")),
			aliased(totalOver(dialect), bareIdentifier("total")))),
		kv("from_", fromItem(baseTable())),
		kv("group", sqlglot.New("Group", kv("expressions", sqlNodes(column.Copy())))))
}
