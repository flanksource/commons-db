package query

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/commons-db/context"
)

// Row is a single result record keyed by column name. It is a type alias for
// the generic map so provider code (ported from duty/dataquery) and CEL
// evaluation can treat rows uniformly.
type Row = map[string]any

// RowPresenter restores a typed record's table presentation after a provider
// has returned it as a generic Row. It is used only for the interactive Clicky
// document; raw exports keep the provider values unchanged.
type RowPresenter interface {
	Columns() []api.ColumnDef
	Present(Row) (map[string]any, error)
}

// Result is the output of executing a Profile: the tabular rows plus any named
// context objects (Policy/Plan/Integrations side panels, each
// produced by a SubQuery).
type Result struct {
	Metrics *MetricMetadata `json:"metrics,omitempty" yaml:"metrics,omitempty"`
	// Profile is the name of the Profile that produced this Result.
	Profile string `json:"profile,omitempty" yaml:"profile,omitempty"`

	// Rows are the primary tabular records.
	Rows []Row `json:"rows" yaml:"rows"`

	// Presenter supplies rich cells for the interactive table without changing
	// the raw values serialized by JSON, CSV, Excel, and other exports.
	Presenter RowPresenter `json:"-" yaml:"-"`

	// Context holds named side objects keyed by SubQuery name.
	Context map[string]any `json:"context,omitempty" yaml:"context,omitempty"`

	// ColumnFilterKeys binds rendered columns to native server filter params.
	ColumnFilterKeys map[string]string `json:"-" yaml:"-"`

	// ColumnSortKeys binds rendered columns to the public name a request sorts
	// them by. A column absent from it cannot be ordered on, which is what makes
	// the table render its header inert rather than offering a sort the server
	// would refuse.
	ColumnSortKeys map[string]string `json:"-" yaml:"-"`

	// Styles holds each row's evaluated ColumnDef.Style classes, keyed by column
	// name and positionally parallel to Rows. It is render-only and deliberately
	// off the wire: styling a cell must not change the value an export carries.
	Styles []map[string]string `json:"-" yaml:"-"`

	// Truncated reports that this result is short of what the query had to
	// give — the Profile's export ceiling stopping the read, or the backend
	// applying a cap of its own. It travels with the Result so a partial answer
	// is never mistaken for a complete one.
	Truncated bool `json:"truncated,omitempty" yaml:"truncated,omitempty"`
}

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
	req, err := buildProviderRequest(ctx, providerRequestInput{Provider: provider, Config: p.Provider, Query: p.Query, Defs: p.Params, Resolved: resolved})
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	req.Filters = filters
	req.Labels = p.Labels
	req.MetricMetadata = &MetricMetadata{}
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
	if len(req.MetricMetadata.Sources) > 0 || len(req.MetricMetadata.Notices) > 0 {
		result.Metrics = req.MetricMetadata
	}

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
	req, err := buildProviderRequest(ctx, providerRequestInput{Provider: provider, Config: sub.Provider, Query: sub.Query, Defs: defs, Resolved: params})
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
type providerRequestInput struct {
	Provider Provider
	Config   ProviderConfig
	Query    string
	Defs     []ParamDef
	Resolved map[string]any
}

func buildProviderRequest(ctx context.Context, in providerRequestInput) (ProviderRequest, error) {
	provider, cfg, rawQuery, defs, resolved := in.Provider, in.Config, in.Query, in.Defs, in.Resolved
	templateParams, err := providerTemplateParams(cfg, defs, resolved)
	if err != nil {
		return ProviderRequest{}, err
	}
	template := newParamTemplate(ctx, templateParams)
	options, err := template.renderOptions(cfg.Options)
	if err != nil {
		return ProviderRequest{}, err
	}
	var queryText string
	var queryArgs []any
	var queryIdentifiers []string
	parameterizer, parameterized := provider.(QueryParameterizer)
	if selector, ok := provider.(QueryParameterizationSelector); ok {
		resolvedConfig := cfg
		resolvedConfig.Options = options
		parameterized = parameterized && selector.ParameterizesQuery(resolvedConfig)
	}
	if parameterized {
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
