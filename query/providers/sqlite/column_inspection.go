package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	querycontext "github.com/flanksource/commons-db/context"
	inspection "github.com/flanksource/commons-db/inspect"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers/internal/providerkit"
	"github.com/flanksource/sqlglot-go/sqlglot"
)

var sqlColumnStatsCache = inspection.NewMemo(inspection.MemoOptions[map[string]int64]{
	Policy: inspection.Policy(inspection.CacheClassCardinality),
	Weight: func(counts map[string]int64) int { return len(counts) },
})

const sqlColumnStatsBatchSize = 25

func (p sqlProvider) InspectColumnFilters(
	ctx querycontext.Context,
	req query.ProviderRequest,
	columns []query.ColumnDef,
) (query.ColumnInspectionResult, error) {
	uniqueFields := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		uniqueFields[column.InspectedField()] = struct{}{}
	}
	fields := make([]string, 0, len(uniqueFields))
	for field := range uniqueFields {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	identity, err := sqlInspectionIdentity(ctx, req)
	if err != nil {
		return query.ColumnInspectionResult{}, err
	}
	key, err := providerkit.InspectionRequestKey("sql-column-stats:"+identity, req, fields)
	if err != nil {
		return query.ColumnInspectionResult{}, err
	}
	result, err := sqlColumnStatsCache.Get(ctx, inspection.GetOptions[map[string]int64]{
		Key: key, Refresh: req.Inspection.Refresh,
		Load: func(fillContext context.Context) (map[string]int64, error) {
			client, dialect, release, err := p.connect(ctx.Wrap(fillContext), req)
			if err != nil {
				return nil, err
			}
			defer release()
			defer client.Close()
			counts := make(map[string]int64, len(fields))
			for _, batch := range providerkit.FieldBatches(fields, sqlColumnStatsBatchSize) {
				built, err := buildColumnStatsSQL(dialect,
					sqlQuery{Text: req.Query, Args: req.QueryArgs, Identifiers: req.QueryIdentifiers}, batch)
				if err != nil {
					return nil, err
				}
				statement, args, err := built.render()
				if err != nil {
					return nil, err
				}
				batchCounts, err := readColumnStats(fillContext, client, renderedSQL{Statement: statement, Args: driverArgs(dialect, args)}, batch)
				if err != nil {
					return nil, err
				}
				for field, count := range batchCounts {
					counts[field] = count
				}
			}
			return counts, nil
		},
	})
	if err != nil && !result.Cache.Cached {
		return query.ColumnInspectionResult{}, fmt.Errorf("inspect SQL column cardinality: %w", err)
	}

	return query.ColumnInspectionResult{
		Filters: sqlFilterSuggestions(columns, result.Value),
		Cache:   []inspection.CacheMetadata{result.Cache},
		Counts:  result.Value,
	}, nil
}

func sqlInspectionIdentity(ctx querycontext.Context, req query.ProviderRequest) (string, error) {
	identity, err := ctx.ConnectionCacheIdentity(req.Connection)
	if err != nil {
		return "", fmt.Errorf("resolve SQL inspection connection identity: %w", err)
	}
	return identity, nil
}

// buildColumnStatsSQL counts the distinct values of every field over the
// wrapped base in one statement.
func buildColumnStatsSQL(dialect sqlDialect, base sqlQuery, fields []string) (*sqlStatement, error) {
	counts := make([]sqlNode, 0, len(fields))
	for _, field := range fields {
		column, err := fieldColumn(field)
		if err != nil {
			return nil, err
		}
		counts = append(counts, countDistinct(dialect, column))
	}
	statement, err := newSQLStatement(dialect)
	if err != nil {
		return nil, err
	}
	tree, err := statement.parseBase(base)
	if err != nil {
		return nil, err
	}
	selection := sqlglot.New("Select", kv("expressions", counts), kv("from_", fromItem(baseTable())))
	if err := wrapAsBaseCTE(tree, selection); err != nil {
		return nil, err
	}
	statement.tree = selection
	return statement, nil
}

func readColumnStats(ctx context.Context, client *sql.DB, rendered renderedSQL, fields []string) (map[string]int64, error) {
	counts := make([]sql.NullInt64, len(fields))
	destinations := make([]any, len(fields))
	for index := range counts {
		destinations[index] = &counts[index]
	}
	if err := client.QueryRowContext(ctx, rendered.Statement, rendered.Args...).Scan(destinations...); err != nil {
		return nil, fmt.Errorf("read column cardinality: %w", err)
	}
	result := make(map[string]int64, len(fields))
	for index, field := range fields {
		if !counts[index].Valid {
			return nil, fmt.Errorf("column %q returned a null distinct count", field)
		}
		result[field] = counts[index].Int64
	}
	return result, nil
}

func sqlFilterSuggestions(columns []query.ColumnDef, counts map[string]int64) map[string]*query.ColumnFilterDef {
	suggestions := make(map[string]*query.ColumnFilterDef)
	for _, column := range columns {
		field := column.InspectedField()
		if counts[field] > query.DefaultFilterLookupLimit {
			suggestions[column.Name] = &query.ColumnFilterDef{Kind: query.ColumnFilterKindText}
		} else if field != column.Name {
			suggestions[column.Name] = &query.ColumnFilterDef{Kind: query.ColumnFilterKindTerms, Field: field}
		}
		if suggestion := suggestions[column.Name]; suggestion != nil && suggestion.Field == "" && field != column.Name {
			suggestion.Field = field
		}
	}
	return suggestions
}
