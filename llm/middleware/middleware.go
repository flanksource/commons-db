package middleware

import (
	"context"

	"github.com/flanksource/commons-db/llm"
)

// Option is a functional option for configuring middleware
type Option func(llm.Provider) llm.Provider

// Wrap wraps a provider with the specified middleware options.
// Options are applied in order, creating a middleware chain.
//
// Example:
//
//	provider := middleware.Wrap(baseProvider,
//	    middleware.WithCache(cacheConfig),
//	    middleware.WithDefaultLogging(),
//	)
func Wrap(provider llm.Provider, options ...Option) llm.Provider {
	for _, option := range options {
		provider = option(provider)
	}
	return provider
}

// contextKey is a type for context keys to avoid collisions
type contextKey string

const (
	// noCacheKey is the context key for bypassing cache
	noCacheKey contextKey = "llm:nocache"
	// correlationIDKey is the context key for correlation IDs
	correlationIDKey contextKey = "llm:correlation_id"
)

// WithNoCache returns a context that bypasses the cache middleware
func WithNoCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, noCacheKey, true)
}

// shouldBypassCache checks if the cache should be bypassed for this context
func shouldBypassCache(ctx context.Context) bool {
	noCache, _ := ctx.Value(noCacheKey).(bool)
	return noCache
}

// WithCorrelationID returns a context with a correlation ID for request tracing
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey, id)
}

// GetCorrelationID retrieves the correlation ID from the context
func GetCorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationIDKey).(string)
	return id
}
