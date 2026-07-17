package query

import (
	"fmt"

	"github.com/flanksource/commons-db/context"
)

// RowIterator is the bounded-memory result source implemented by providers
// that can keep a backend cursor open while callers consume one row at a time.
type RowIterator interface {
	Next() bool
	Row() Row
	Err() error
	Close() error
}

// StreamingProvider is an optional Provider capability. Providers that do not
// implement it continue to work through Execute's buffered compatibility path.
type StreamingProvider interface {
	Provider
	OpenRows(ctx context.Context, req ProviderRequest) (RowIterator, error)
}

// SupportsStreaming reports whether the registered provider can open a row
// cursor without materializing its complete result.
func SupportsStreaming(providerType string) bool {
	provider, err := GetProvider(providerType)
	if err != nil {
		return false
	}
	_, ok := provider.(StreamingProvider)
	return ok
}

// ExecuteRows resolves profile parameters and templates exactly like Execute,
// but opens a provider cursor and applies CEL columns one row at a time.
func ExecuteRows(ctx context.Context, p Profile, params ...map[string]any) (RowIterator, error) {
	return executeRows(ctx, p, 0, params...)
}

// ExecuteRowsBounded opens a provider row source that will be consumed for at
// most maxRows rows. Providers can use the hint to avoid expensive cursors.
func ExecuteRowsBounded(ctx context.Context, p Profile, maxRows int, params ...map[string]any) (RowIterator, error) {
	if maxRows <= 0 {
		return nil, fmt.Errorf("max rows must be greater than zero")
	}
	return executeRows(ctx, p, maxRows, params...)
}

func executeRows(ctx context.Context, p Profile, maxRows int, params ...map[string]any) (RowIterator, error) {
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
	provider, err := GetProvider(p.Provider.Type)
	if err != nil {
		return nil, err
	}
	streaming, ok := provider.(StreamingProvider)
	if !ok {
		return nil, fmt.Errorf("provider %q does not support streaming rows", p.Provider.Type)
	}
	rendered, err := renderQuery(ctx, p.Query, resolved)
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", p.Name, err)
	}
	rows, err := streaming.OpenRows(ctx, ProviderRequest{
		Connection: p.Provider.Connection,
		Query:      rendered,
		Options:    p.Provider.Options,
		Params:     resolved,
		MaxRows:    maxRows,
	})
	if err != nil {
		return nil, fmt.Errorf("profile %q: provider %q failed: %w", p.Name, p.Provider.Type, err)
	}
	return &columnRowIterator{ctx: ctx, profile: p, rows: rows}, nil
}

type columnRowIterator struct {
	ctx     context.Context
	profile Profile
	rows    RowIterator
	row     Row
	err     error
	index   int
}

func (i *columnRowIterator) Next() bool {
	if i.err != nil || !i.rows.Next() {
		return false
	}
	i.row = i.rows.Row()
	if err := applyRowTransforms(i.ctx, i.profile, []Row{i.row}); err != nil {
		i.err = fmt.Errorf("profile %q: row %d: %w", i.profile.Name, i.index, err)
		return false
	}
	i.index++
	return true
}

func (i *columnRowIterator) Row() Row { return i.row }
func (i *columnRowIterator) Err() error {
	if i.err != nil {
		return i.err
	}
	return i.rows.Err()
}
func (i *columnRowIterator) Close() error { return i.rows.Close() }

// SliceRows adapts an already-buffered result to RowIterator. It is useful for
// processors and providers that require the legacy whole-result pipeline.
func SliceRows(rows []Row) RowIterator { return &sliceRowIterator{rows: rows} }

type sliceRowIterator struct {
	rows  []Row
	index int
}

func (i *sliceRowIterator) Next() bool {
	if i.index >= len(i.rows) {
		return false
	}
	i.index++
	return true
}
func (i *sliceRowIterator) Row() Row     { return i.rows[i.index-1] }
func (i *sliceRowIterator) Err() error   { return nil }
func (i *sliceRowIterator) Close() error { return nil }
