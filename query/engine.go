package query

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

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
	if err := p.Validate(); err != nil {
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
	resolved, filters, err := resolveProfileInput(p, supplied)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	return executeResolved(ctx, p, resolved, filters)
}

// executeResolved runs the post-param pipeline: render → provider → columns →
// context sub-queries → processors (→ top sort/limit). Shared by Execute and
// each top-session tick.
func executeResolved(ctx context.Context, p Profile, resolved map[string]any, filters []ColumnFilterValue) (*Result, error) {
	provider, err := GetProvider(p.Provider.Type)
	if err != nil {
		return nil, err
	}

	req, err := buildProviderRequest(ctx, p.Provider, p.Query, p.Params, resolved)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	req.Filters = filters

	rows, err := provider.Execute(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("profile %q: provider %q failed: %w", p.Name, p.Provider.Type, err)
	}

	if err := applyRowTransforms(ctx, p, rows); err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}

	result := &Result{Profile: p.Name, Rows: rows}

	for name, sub := range p.Context {
		subRows, err := executeSubQuery(ctx, sub, p.Params, resolved)
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
	result.ColumnFilterKeys, err = p.ColumnFilterKeys()
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
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

// executeSubQuery runs a context sub-query against the parent profile's already
// resolved params, which carry the parent's declarations with them.
func executeSubQuery(ctx context.Context, sub SubQuery, defs []ParamDef, params map[string]any) ([]Row, error) {
	provider, err := GetProvider(sub.Provider.Type)
	if err != nil {
		return nil, err
	}
	req, err := buildProviderRequest(ctx, sub.Provider, sub.Query, defs, params)
	if err != nil {
		return nil, err
	}
	return provider.Execute(ctx, req)
}

// buildProviderRequest renders everything the provider is handed — the query,
// the provider options and the connection — against the resolved params, so
// every provider type supports the same `{{.params.x}}` / `$(.params.x)`
// interpolation instead of only those driven by a query string. Callers set
// Filters and MaxRows on the result.
func buildProviderRequest(ctx context.Context, cfg ProviderConfig, rawQuery string, defs []ParamDef, resolved map[string]any) (ProviderRequest, error) {
	template := newParamTemplate(ctx, resolved)
	query, err := template.render("query", rawQuery)
	if err != nil {
		return ProviderRequest{}, err
	}
	options, err := template.renderOptions(cfg.Options)
	if err != nil {
		return ProviderRequest{}, err
	}
	connection, err := template.render("provider.connection", cfg.Connection)
	if err != nil {
		return ProviderRequest{}, err
	}
	return ProviderRequest{
		Connection:      connection,
		Query:           query,
		Options:         options,
		Params:          resolved,
		ParamRoles:      paramRoles(defs),
		TemplatedParams: template.usedParams(),
	}, nil
}
