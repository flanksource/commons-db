package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	querycontext "github.com/flanksource/commons-db/context"
	inspection "github.com/flanksource/commons-db/inspect"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers/internal/providerkit"
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
				statement, err := buildColumnStatsSQL(dialect, req.Query, batch)
				if err != nil {
					return nil, err
				}
				statement, err = materializeSQLParams(dialect, statement, req.QueryArgs, req.QueryIdentifiers)
				if err != nil {
					return nil, err
				}
				batchCounts, err := readColumnStats(fillContext, client, statement, req.QueryArgs, batch)
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

func buildColumnStatsSQL(dialect sqlDialect, statement string, fields []string) (string, error) {
	counts := make([]string, 0, len(fields))
	for _, field := range fields {
		quoted, err := dialect.quote(field)
		if err != nil {
			return "", err
		}
		counts = append(counts, "COUNT(DISTINCT "+quoted+")")
	}
	return wrapAsBaseCTE(dialect, statement, func(base string) string {
		return "SELECT " + strings.Join(counts, ", ") + "\nFROM " + base
	})
}

func readColumnStats(ctx context.Context, client *sql.DB, statement string, args []any, fields []string) (map[string]int64, error) {
	counts := make([]sql.NullInt64, len(fields))
	destinations := make([]any, len(fields))
	for index := range counts {
		destinations[index] = &counts[index]
	}
	if err := client.QueryRowContext(ctx, statement, args...).Scan(destinations...); err != nil {
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
