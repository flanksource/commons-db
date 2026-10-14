// Package providerkit holds the helpers shared by the query provider packages,
// so a provider split into its own package (sqlite) and the bundle
// (query/providers) use one implementation without importing each other.
package providerkit

import (
	"fmt"

	"github.com/flanksource/commons-db/context"
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
