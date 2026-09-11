package providers

import (
	"fmt"
	"strings"

	"github.com/Masterminds/squirrel"
	"github.com/flanksource/commons-db/query"
)

const (
	sqlArrayItem   = "__cdb_item"
	sqlArrayMember = "__cdb_value"
	sqlArrayRows   = "__cdb_rows"
	sqlArrayRow    = "__cdb_row"
	sqlArrayValue  = "__cdb_array"
)

type sqlArrayExpansion struct {
	from         string
	value        string
	stringMember string
}

func sqlArrayExpansionFor(dialect sqlDialect, array string) (sqlArrayExpansion, error) {
	item := dialect.quoteValidatedIdentifier(sqlArrayItem)
	member := dialect.quoteValidatedIdentifier(sqlArrayMember)
	switch dialect {
	case dialectSQLite:
		return sqlArrayExpansion{
			from:         fmt.Sprintf("json_each(%s) AS %s", array, item),
			value:        item + ".value",
			stringMember: item + ".type = 'text'",
		}, nil
	case dialectPostgres:
		jsonValue := item + "." + member
		return sqlArrayExpansion{
			from: fmt.Sprintf(
				"jsonb_array_elements(COALESCE(NULLIF(to_jsonb(%s), 'null'::jsonb), '[]'::jsonb)) AS %s(%s)",
				array, item, member),
			value:        "(" + jsonValue + " #>> '{}')",
			stringMember: "jsonb_typeof(" + jsonValue + ") = 'string'",
		}, nil
	case dialectMySQL:
		jsonValue := item + "." + member
		return sqlArrayExpansion{
			from: fmt.Sprintf(
				"JSON_TABLE(IF(JSON_TYPE(%s) = 'ARRAY', %s, JSON_ARRAY()), '$[*]' COLUMNS (%s JSON PATH '$')) AS %s",
				array, array, member, item),
			value:        "JSON_UNQUOTE(" + jsonValue + ")",
			stringMember: "JSON_TYPE(" + jsonValue + ") = 'STRING'",
		}, nil
	case dialectSQLServer:
		return sqlArrayExpansion{
			from:         fmt.Sprintf("OPENJSON(COALESCE(CONVERT(nvarchar(max), %s), N'[]')) AS %s", array, item),
			value:        item + ".[value]",
			stringMember: item + ".[type] = 1",
		}, nil
	default:
		return sqlArrayExpansion{}, fmt.Errorf("the %s dialect has no array expansion", dialect)
	}
}

// sqlArrayPredicate keeps a row whose array holds any included string, and
// drops one whose array holds any excluded string. SQL NULL and an empty array
// hold no values, so exclusion keeps both. JSON backends deliberately ignore
// non-string members because filter values have a string identity.
func sqlArrayPredicate(dialect sqlDialect, column string, filter query.ColumnFilterValue) (squirrel.Sqlizer, error) {
	if kind := filter.Kind.CompilesAs(); kind != query.ColumnFilterKindTerms {
		return nil, fmt.Errorf("field %q is an array, which only a value selection compares; got a %s filter",
			filter.Field, filter.Kind.Normalized())
	}
	if dialect == dialectClickHouse {
		return clickHouseArrayPredicate(column, filter), nil
	}
	expansion, err := sqlArrayExpansionFor(dialect, column)
	if err != nil {
		return nil, fmt.Errorf("field %q is an array: %w", filter.Field, err)
	}
	conditions := squirrel.And{}
	for _, side := range []struct {
		values []string
		prefix string
	}{{filter.Include, "EXISTS"}, {filter.Exclude, "NOT EXISTS"}} {
		if len(side.values) == 0 {
			continue
		}
		args := make([]any, len(side.values))
		for index, value := range side.values {
			args[index] = value
		}
		markers := strings.TrimRight(strings.Repeat("?,", len(args)), ",")
		members := fmt.Sprintf("SELECT 1 FROM %s WHERE (%s AND %s IN (%s))",
			expansion.from, expansion.stringMember, expansion.value, markers)
		conditions = append(conditions, squirrel.Expr(side.prefix+" ("+members+")", args...))
	}
	if len(conditions) == 0 {
		return nil, nil
	}
	return unwrapSingle(conditions), nil
}

func clickHouseArrayPredicate(column string, filter query.ColumnFilterValue) squirrel.Sqlizer {
	conditions := squirrel.And{}
	if len(filter.Include) > 0 {
		included := squirrel.Or{}
		for _, value := range filter.Include {
			included = append(included, squirrel.Expr("has("+column+", ?)", value))
		}
		if len(included) == 1 {
			conditions = append(conditions, included[0])
		} else {
			conditions = append(conditions, included)
		}
	}
	for _, value := range filter.Exclude {
		conditions = append(conditions, squirrel.Expr("NOT has("+column+", ?)", value))
	}
	return unwrapSingle(conditions)
}

func buildArrayLookupSQL(
	dialect sqlDialect,
	statement string,
	baseArgCount int,
	binding query.ColumnFilterBinding,
	siblings []query.ColumnFilterValue,
	search string,
	limit int,
) (string, []any, error) {
	column, err := dialect.quote(binding.Field)
	if err != nil {
		return "", nil, err
	}
	scope, args, err := filterClause(dialect, siblings)
	if err != nil {
		return "", nil, err
	}
	scope = offsetPlaceholders(dialect, scope, baseArgCount)
	if scope != "" {
		scope = " WHERE " + scope
	}
	if dialect == dialectClickHouse {
		return buildClickHouseArrayLookupSQL(dialect, statement, column, scope, args, baseArgCount, search, limit)
	}
	rows := dialect.quoteValidatedIdentifier(sqlArrayRows)
	row := dialect.quoteValidatedIdentifier(sqlArrayRow)
	array := dialect.quoteValidatedIdentifier(sqlArrayValue)
	expansion, err := sqlArrayExpansionFor(dialect, rows+"."+array)
	if err != nil {
		return "", nil, fmt.Errorf("field %q is an array: %w", binding.Field, err)
	}
	where, searchArgs, err := arrayLookupWhere(dialect, expansion, search, baseArgCount+len(args))
	if err != nil {
		return "", nil, err
	}
	join := "CROSS JOIN "
	switch dialect {
	case dialectPostgres:
		join = "CROSS JOIN LATERAL "
	case dialectSQLServer:
		join = "CROSS APPLY "
	}
	wrapped, err := wrapAsBaseCTE(dialect, statement, func(base string) string {
		return strings.Join([]string{
			fmt.Sprintf("SELECT %s AS value, COUNT(DISTINCT %s.%s) AS count, COUNT(*) OVER () AS total", expansion.value, rows, row),
			fmt.Sprintf("FROM (SELECT ROW_NUMBER() OVER (ORDER BY (SELECT NULL)) AS %s, %s AS %s FROM %s%s) AS %s %s%s",
				row, column, array, base, scope, rows, join, expansion.from),
			"WHERE " + where,
			"GROUP BY " + expansion.value,
			"ORDER BY 2 DESC, 1 ASC",
			dialect.limitTail(limit),
		}, "\n")
	})
	if err != nil {
		return "", nil, err
	}
	return wrapped, append(args, searchArgs...), nil
}

func arrayLookupWhere(
	dialect sqlDialect,
	expansion sqlArrayExpansion,
	search string,
	placeholderOffset int,
) (string, []any, error) {
	conditions := squirrel.And{
		squirrel.Expr(expansion.stringMember),
		squirrel.Expr(expansion.value + " IS NOT NULL"),
	}
	if search != "" {
		predicate, pattern := dialect.likeMatch(expansion.value, search)
		conditions = append(conditions, squirrel.Expr(predicate, pattern))
	}
	where, args, err := renderClause(dialect, unwrapSingle(conditions))
	return offsetPlaceholders(dialect, where, placeholderOffset), args, err
}

func buildClickHouseArrayLookupSQL(
	dialect sqlDialect,
	statement, column, scope string,
	args []any,
	baseArgCount int,
	search string,
	limit int,
) (string, []any, error) {
	rows := dialect.quoteValidatedIdentifier(sqlArrayRows)
	array := dialect.quoteValidatedIdentifier(sqlArrayValue)
	item := dialect.quoteValidatedIdentifier(sqlArrayItem)
	conditions := squirrel.And{squirrel.Expr(item + " IS NOT NULL")}
	if search != "" {
		predicate, pattern := dialect.likeMatch(item, search)
		conditions = append(conditions, squirrel.Expr(predicate, pattern))
	}
	where, searchArgs, err := renderClause(dialect, unwrapSingle(conditions))
	if err != nil {
		return "", nil, err
	}
	where = offsetPlaceholders(dialect, where, baseArgCount+len(args))
	wrapped, err := wrapAsBaseCTE(dialect, statement, func(base string) string {
		return strings.Join([]string{
			fmt.Sprintf("SELECT %s AS value, COUNT(*) AS count, COUNT(*) OVER () AS total", item),
			fmt.Sprintf("FROM (SELECT %s AS %s FROM %s%s) AS %s ARRAY JOIN arrayDistinct(%s.%s) AS %s",
				column, array, base, scope, rows, rows, array, item),
			"WHERE " + where,
			"GROUP BY " + item,
			"ORDER BY 2 DESC, 1 ASC",
			dialect.limitTail(limit),
		}, "\n")
	})
	if err != nil {
		return "", nil, err
	}
	return wrapped, append(args, searchArgs...), nil
}
