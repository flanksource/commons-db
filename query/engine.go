package query

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/flanksource/gomplate/v3"

	"github.com/flanksource/commons-db/context"
)

// Execute runs a Profile end-to-end: resolve the supplied params, render the
// query, dispatch to the provider, evaluate CEL columns, run any context
// SubQueries, and apply processors.
//
// params carries the server-side filter values for the Profile's declared Params
// (omit when there are none). They are validated/coerced against the
// declarations and exposed to the query template as `params`.
func Execute(ctx context.Context, p Profile, params ...map[string]any) (*Result, error) {
	if err := p.ValidateKind(); err != nil {
		return nil, err
	}
	if p.Kind() == KindTrace {
		return nil, fmt.Errorf("profile %q is a trace; use ExecuteStream", p.Name)
	}
	if p.Namespace != "" {
		ctx = ctx.WithNamespace(p.Namespace)
	}
	var supplied map[string]any
	if len(params) > 0 {
		supplied = params[0]
	}
	resolved, err := resolveParams(p.Params, supplied)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	return executeResolved(ctx, p, resolved)
}

// executeResolved runs the post-param pipeline: render → provider → columns →
// context sub-queries → processors (→ top sort/limit). Shared by Execute and
// each top-session tick.
func executeResolved(ctx context.Context, p Profile, resolved map[string]any) (*Result, error) {
	provider, err := GetProvider(p.Provider.Type)
	if err != nil {
		return nil, err
	}

	query, err := renderQuery(ctx, p.Query, resolved)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}

	rows, err := provider.Execute(ctx, ProviderRequest{
		Connection: p.Provider.Connection,
		Query:      query,
		Options:    p.Provider.Options,
		Params:     resolved,
	})
	if err != nil {
		return nil, fmt.Errorf("profile %q: provider %q failed: %w", p.Name, p.Provider.Type, err)
	}

	if err := applyRowTransforms(ctx, p, rows); err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}

	result := &Result{Profile: p.Name, Rows: rows}

	for name, sub := range p.Context {
		subRows, err := executeSubQuery(ctx, sub, resolved)
		if err != nil {
			return nil, fmt.Errorf("profile %q: context %q failed: %w", p.Name, name, err)
		}
		if result.Context == nil {
			result.Context = map[string]any{}
		}
		result.Context[name] = subRows
	}

	result, err = applyProcessors(ctx, p.Processors, result)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}

	if p.Top != nil {
		result.Rows = sortAndLimit(result.Rows, p.Top.SortBy, p.Top.Limit)
	}
	return result, nil
}

// sortAndLimit orders rows descending by the named column, then truncates to
// limit. Zero values leave the rows untouched.
func sortAndLimit(rows []Row, sortBy string, limit int) []Row {
	if sortBy != "" {
		sort.SliceStable(rows, func(i, j int) bool {
			return compareRowValues(rows[i][sortBy], rows[j][sortBy]) > 0
		})
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// compareRowValues orders numbers numerically and everything else by its
// string form.
func compareRowValues(a, b any) int {
	af, aok := toFloat(a)
	bf, bok := toFloat(b)
	if aok && bok {
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		default:
			return 0
		}
	}
	return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case string:
		// SQL drivers surface numeric/decimal columns as strings.
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func executeSubQuery(ctx context.Context, sub SubQuery, params map[string]any) ([]Row, error) {
	provider, err := GetProvider(sub.Provider.Type)
	if err != nil {
		return nil, err
	}
	query, err := renderQuery(ctx, sub.Query, params)
	if err != nil {
		return nil, err
	}
	return provider.Execute(ctx, ProviderRequest{
		Connection: sub.Provider.Connection,
		Query:      query,
		Options:    sub.Provider.Options,
		Params:     params,
	})
}

// renderQuery templates the query with the resolved params under `params`. It is
// a no-op when the query contains no template delimiters, so plain queries pass
// through untouched.
func renderQuery(ctx context.Context, query string, params map[string]any) (string, error) {
	if !strings.Contains(query, "{{") && !strings.Contains(query, "$(") {
		return query, nil
	}
	return ctx.RunTemplate(gomplate.Template{Template: query}, map[string]any{"params": params})
}
