package middleware

import (
	"context"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/llm"
	"github.com/flanksource/commons-db/llm/cache"
)

// CacheConfig holds configuration for caching middleware
type CacheConfig struct {
	Cache *cache.Cache  // Cache instance (required)
	TTL   time.Duration // Time-to-live for cache entries
}

// cachingProvider wraps a Provider with caching capabilities
type cachingProvider struct {
	provider llm.Provider
	cache    *cache.Cache
	ttl      time.Duration
}

// newCachingProvider creates a new caching middleware
func newCachingProvider(provider llm.Provider, config CacheConfig) (llm.Provider, error) {
	if config.Cache == nil {
		return nil, fmt.Errorf("cache instance is required")
	}

	return &cachingProvider{
		provider: provider,
		cache:    config.Cache,
		ttl:      config.TTL,
	}, nil
}

// Execute implements the Provider interface with caching
func (c *cachingProvider) Execute(ctx context.Context, req llm.ProviderRequest) (llm.ProviderResponse, error) {
	// Check if cache should be bypassed
	if shouldBypassCache(ctx) {
		return c.provider.Execute(ctx, req)
	}

	// Default values for cache key generation
	temperature := 0.2
	maxTokens := 0
	if req.MaxTokens != nil {
		maxTokens = *req.MaxTokens
	}

	// Try to get from cache
	cachedEntry, err := c.cache.Get(req.Prompt, req.Model, temperature, maxTokens)
	if err == nil && cachedEntry != nil && cachedEntry.Error == "" {
		// Cache hit - return cached response
		resp := llm.ProviderResponse{
			Text:         cachedEntry.Response,
			Model:        cachedEntry.Model,
			InputTokens:  cachedEntry.TokensInput,
			OutputTokens: cachedEntry.TokensOutput,
		}

		if cachedEntry.TokensReasoning > 0 {
			resp.ReasoningTokens = &cachedEntry.TokensReasoning
		}
		if cachedEntry.TokensCacheRead > 0 {
			resp.CacheReadTokens = &cachedEntry.TokensCacheRead
		}
		if cachedEntry.TokensCacheWrite > 0 {
			resp.CacheWriteTokens = &cachedEntry.TokensCacheWrite
		}

		return resp, nil
	}

	// Cache miss - execute request
	startTime := time.Now()
	resp, execErr := c.provider.Execute(ctx, req)
	duration := time.Since(startTime)

	// Prepare cache entry
	cacheEntry := &cache.Entry{
		Model:       req.Model,
		Prompt:      req.Prompt,
		Temperature: temperature,
		MaxTokens:   maxTokens,
		DurationMS:  int64(duration.Milliseconds()),
		CreatedAt:   time.Now(),
	}

	if execErr != nil {
		// Cache the error
		cacheEntry.Error = execErr.Error()
		if err := c.cache.Set(cacheEntry); err != nil {
			// Log cache error but don't fail the request
			fmt.Printf("Warning: failed to cache error: %v\n", err)
		}
		return resp, execErr
	}

	// Populate cache entry with successful response
	cacheEntry.Response = resp.Text
	cacheEntry.TokensInput = resp.InputTokens
	cacheEntry.TokensOutput = resp.OutputTokens
	cacheEntry.TokensTotal = resp.InputTokens + resp.OutputTokens

	if resp.ReasoningTokens != nil {
		cacheEntry.TokensReasoning = *resp.ReasoningTokens
		cacheEntry.TokensTotal += *resp.ReasoningTokens
	}
	if resp.CacheReadTokens != nil {
		cacheEntry.TokensCacheRead = *resp.CacheReadTokens
	}
	if resp.CacheWriteTokens != nil {
		cacheEntry.TokensCacheWrite = *resp.CacheWriteTokens
	}

	// Calculate cost (simplified - should use actual cost calculation from llm package)
	// This is a placeholder - the actual cost calculation should be imported from llm.cost
	cacheEntry.CostUSD = calculateApproximateCost(resp.InputTokens, resp.OutputTokens)

	// Store in cache
	if err := c.cache.Set(cacheEntry); err != nil {
		// Log cache error but don't fail the request
		fmt.Printf("Warning: failed to cache response: %v\n", err)
	}

	return resp, nil
}

// calculateApproximateCost is a placeholder for cost calculation
// TODO: Use the actual cost calculation from llm.calculateCost
func calculateApproximateCost(inputTokens, outputTokens int) float64 {
	// Rough estimate: $0.000003 per input token, $0.000015 per output token
	// This should be replaced with actual model-specific pricing
	return (float64(inputTokens) * 0.000003) + (float64(outputTokens) * 0.000015)
}

// WithCache returns a middleware option that adds caching capabilities
func WithCache(config CacheConfig) Option {
	return func(p llm.Provider) llm.Provider {
		provider, err := newCachingProvider(p, config)
		if err != nil {
			// If cache creation fails, return the original provider
			fmt.Printf("Warning: failed to create caching middleware: %v\n", err)
			return p
		}
		return provider
	}
}

// WithCacheInstance is a convenience function for creating cache middleware with just a cache instance
func WithCacheInstance(cache *cache.Cache) Option {
	return WithCache(CacheConfig{
		Cache: cache,
		TTL:   24 * time.Hour,
	})
}
