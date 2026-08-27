package query

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/commons-db/context"
)

// Execute runs a Profile end-to-end: resolve the supplied params, render the
// query, dispatch to the provider, run processors, and then evaluate aliases,
// filters, columns, and styles. Context SubQueries are available to processors.
//
// params carries the server-side filter values for the Profile's declared Params
// (omit when there are none). They are validated/coerced against the
// declarations and exposed to the query template as `params`.
//
// The read stops at the Profile's own MaxExportRows, and Result.Truncated says
// whether stopping there left rows behind. Every buffered caller — the CLI, a
// replay, a reconcile that cannot merge — is asking for the whole result, and
// "the whole result" against an unbounded source is not a number any of them
// can hold. The ceiling is the profile's to raise.
func Execute(ctx context.Context, p Profile, params ...map[string]any) (*Result, error) {
	var supplied map[string]any
	if len(params) > 0 {
		supplied = params[0]
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if p.Kind() == KindTrace {
		return nil, fmt.Errorf("profile %q is a trace; use ExecuteStream", p.Name)
	}
	if p.Namespace != "" {
		ctx = ctx.WithNamespace(p.Namespace)
	}
	resolved, filters, err := resolveProfileInput(p, supplied, time.Now())
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	return executeResolved(ctx, p, resolved, filters)
}

// executeResolved runs the post-param pipeline: render → provider → context
// sub-queries → processors → row mapping (→ top sort/limit). Shared by Execute
// and each top-session tick.
func executeResolved(ctx context.Context, p Profile, resolved map[string]any, filters []ColumnFilterValue) (*Result, error) {
	provider, err := GetProvider(p.Provider.Type)
	if err != nil {
		return nil, err
	}
	req, err := buildProviderRequest(ctx, provider, p.Provider, p.Query, p.Params, resolved)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	req.Filters = filters
	req.Order, err = p.EffectiveOrder()
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	req.Diagnostics = DiagnosticSink(ctx)
	ctx, req, operation, err := prepareConnectionOperation(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}

	rows, truncated, err := drainPages(ctx, p, req)
	operation.Finish(len(rows), err)
	if err != nil {
		return nil, err
	}

	result := &Result{Profile: p.Name, Rows: rows, Truncated: truncated}

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
	result.Rows, result.Styles, err = applyRowTransforms(ctx, p, result.Rows)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	if p.Top != nil {
		var cut bool
		result.Rows, cut = sortAndLimit(result.Rows, p.Top.SortBy, p.Top.Limit)
		result.Truncated = result.Truncated || cut
	}
	result.ColumnFilterKeys, err = p.ColumnFilterKeys()
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	result.ColumnSortKeys, err = p.ColumnSortKeys()
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	return result, nil
}

// sortAndLimit orders rows descending by the named column, then truncates to
// limit. Zero values leave the rows untouched. The second return reports that
// the cut actually removed rows, so a snapshot that dropped the tail is not
// presented as the whole of it.
func sortAndLimit(rows []Row, sortBy string, limit int) ([]Row, bool) {
	if sortBy != "" {
		sort.SliceStable(rows, func(i, j int) bool {
			return compareRowValues(rows[i][sortBy], rows[j][sortBy]) > 0
		})
	}
	if limit > 0 && len(rows) > limit {
		return rows[:limit], true
	}
	return rows, false
}

// drainPages walks every page of the provider's result, stopping at the
// Profile's export ceiling.
//
// truncated reports that the walk did not see all the query had. Two separate
// things cause that and both matter: the ceiling cutting the read, and the
// provider applying a cap of its own. The second is the one that used to be
// invisible — a backend default quietly limiting a read is otherwise
// indistinguishable from a small table, which is how "read everything" came to
// mean "read the first 500 and say nothing".
func drainPages(ctx context.Context, p Profile, req ProviderRequest) ([]Row, bool, error) {
	maxRows := p.RowLimits().MaxExportRows
	batch := walkBatchSize
	if maxRows > 0 && maxRows < batch {
		batch = maxRows
	}
	walk := walkRequest(p, batch)

	var rows []Row
	var truncated bool
	for page, err := range providerPages(ctx, p, req, walk) {
		if err != nil {
			return nil, false, err
		}
		truncated = truncated || page.Truncated
		rows = append(rows, page.Rows...)
		if maxRows > 0 && len(rows) >= maxRows {
			truncated = truncated || len(rows) > maxRows || page.HasMore
			rows = rows[:maxRows]
			break
		}
	}
	return rows, truncated, nil
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
	req, err := buildProviderRequest(ctx, provider, sub.Provider, sub.Query, defs, params)
	if err != nil {
		return nil, err
	}
	ctx, req, operation, err := prepareConnectionOperation(ctx, req)
	if err != nil {
		return nil, err
	}
	rows, err := provider.Execute(ctx, req)
	operation.Finish(len(rows), err)
	return rows, err
}

// buildProviderRequest resolves the query, provider options and connection
// against the profile params. Providers with structural query binding receive
// values separately from their query text. Callers set Filters, Order and
// Position on the result.
func buildProviderRequest(ctx context.Context, provider Provider, cfg ProviderConfig, rawQuery string, defs []ParamDef, resolved map[string]any) (ProviderRequest, error) {
	templateParams, err := providerTemplateParams(cfg, defs, resolved)
	if err != nil {
		return ProviderRequest{}, err
	}
	template := newParamTemplate(ctx, templateParams)
	var queryText string
	var queryArgs []any
	var queryIdentifiers []string
	if parameterizer, ok := provider.(QueryParameterizer); ok {
		var parameterized ParameterizedQuery
		parameterized, err = parameterizer.ParameterizeQuery(QueryParameterizationRequest{
			Query: rawQuery, Params: templateParams, Definitions: defs,
		})
		if err != nil {
			return ProviderRequest{}, fmt.Errorf("query: %w", err)
		}
		queryText = parameterized.Query
		queryArgs = parameterized.Args
		queryIdentifiers = parameterized.Identifiers
		for _, name := range parameterized.UsedParams {
			template.used[name] = true
		}
	} else {
		queryText, err = template.render("query", rawQuery)
		if err != nil {
			return ProviderRequest{}, err
		}
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
		Provider:         cfg.Type,
		Connection:       connection,
		Query:            queryText,
		QueryArgs:        queryArgs,
		QueryIdentifiers: queryIdentifiers,
		Options:          options,
		Params:           resolved,
		ParamRoles:       paramRoles(defs),
		TemplatedParams:  template.usedParams(),
	}, nil
}
