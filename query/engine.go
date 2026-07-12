package query

import (
	"fmt"
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
	})
	if err != nil {
		return nil, fmt.Errorf("profile %q: provider %q failed: %w", p.Name, p.Provider.Type, err)
	}

	if err := applyColumns(ctx, p.Columns, rows); err != nil {
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

	return result, nil
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
