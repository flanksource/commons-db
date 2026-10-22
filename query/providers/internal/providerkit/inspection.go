package providerkit

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// ResolveInlineURL resolves an inline connection URL for connectionType; an
// empty raw URL resolves to empty.
func ResolveInlineURL(ctx context.Context, raw, connectionType string) (string, error) {
	if raw == "" {
		return "", nil
	}
	resolved, err := ctx.ResolveConnectionURL(raw, connectionType, ctx.GetNamespace())
	if err != nil {
		return "", fmt.Errorf("resolve inline %s url: %w", connectionType, err)
	}
	return resolved, nil
}

// OrderKeys reads a row's position in the declared order, which is what the
// next page resumes after.
func OrderKeys(row query.Row, order query.Order) ([]any, error) {
	keys := make([]any, 0, len(order))
	for _, by := range order {
		value, ok := row[by.Column]
		if !ok {
			return nil, fmt.Errorf(
				"cannot cursor on column %q: the query does not return it, so there is no position to resume from", by.Column)
		}
		keys = append(keys, value)
	}
	return keys, nil
}

// FieldBatches splits fields into consecutive batches of at most size.
func FieldBatches(fields []string, size int) [][]string {
	if size <= 0 {
		panic("inspection field batch size must be positive")
	}
	count := len(fields) / size
	if len(fields)%size != 0 {
		count++
	}
	batches := make([][]string, 0, count)
	for start := 0; start < len(fields); start += size {
		batches = append(batches, fields[start:min(start+size, len(fields))])
	}
	return batches
}

// InspectionRequestKey is the column-inspection cache key for req over fields.
func InspectionRequestKey(prefix string, req query.ProviderRequest, fields []string) (string, error) {
	payload, err := json.Marshal(struct {
		Provider         string
		Connection       string
		Query            string
		QueryArgs        []any
		QueryIdentifiers []string
		Options          map[string]any
		Params           map[string]any
		Fields           []string
	}{
		Provider: req.Provider, Connection: req.Connection, Query: req.Query,
		QueryArgs: req.QueryArgs, QueryIdentifiers: req.QueryIdentifiers,
		Options: req.Options, Params: req.Params, Fields: fields,
	})
	if err != nil {
		return "", fmt.Errorf("encode inspection cache key: %w", err)
	}
	return fmt.Sprintf("%s:%x", prefix, sha256.Sum256(payload)), nil
}
