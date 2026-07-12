package providers

import (
	"fmt"

	"github.com/flanksource/commons-db/context"
)

func resolveInlineURL(ctx context.Context, raw, connectionType string) (string, error) {
	if raw == "" {
		return "", nil
	}
	resolved, err := ctx.ResolveConnectionURL(raw, connectionType, ctx.GetNamespace())
	if err != nil {
		return "", fmt.Errorf("resolve inline %s url: %w", connectionType, err)
	}
	return resolved, nil
}
