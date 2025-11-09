package middleware

import (
	"context"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/llm/cache"
	. "github.com/flanksource/commons-db/llm/types"
	"github.com/flanksource/commons/logger"
)

// CacheConfig holds configuration for caching middleware
type CacheConfig struct {
	Cache *cache.Cache // Cache instance (required)
}

// cachingProvider wraps a Provider with caching capabilities
type cachingProvider struct {
	provider Provider
	cache    *cache.Cache
}

// newCachingProvider creates a new caching middleware
func newCachingProvider(provider Provider, config ...CacheConfig) (Provider, error) {
	if len(config) == 0 || config[0].Cache == nil {
		c, err := cache.New(cache.Config{
			TTL: 24 * time.Hour,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create cache: %w", err)
		}
		config = []CacheConfig{
			{
				Cache: c,
			},
		}
	}

	if config[0].Cache == nil {
		return nil, fmt.Errorf("cache instance is required")
	}

	return &cachingProvider{
		provider: provider,
		cache:    config[0].Cache,
	}, nil
}

// GetModel returns the model name from the wrapped provider
func (c *cachingProvider) GetModel() string {
	return c.provider.GetModel()
}

// GetBackend returns the backend type from the wrapped provider
func (c *cachingProvider) GetBackend() LLMBackend {
	return c.provider.GetBackend()
}

// Execute implements the Provider interface with caching
func (c *cachingProvider) Execute(ctx context.Context, req ProviderRequest) (ProviderResponse, error) {
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
		resp := ProviderResponse{
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

	// Calculate cost using actual pricing from llm package
	costInfo, err := CalculateCost(req.Model, resp.InputTokens, resp.OutputTokens,
		resp.ReasoningTokens, resp.CacheReadTokens, resp.CacheWriteTokens)
	if err == nil {
		cacheEntry.CostUSD = costInfo.Cost
	} else {
		// If cost calculation fails, log warning but continue caching
		fmt.Printf("Warning: failed to calculate cost: %v\n", err)
	}

	// Infer provider from model
	cacheEntry.Provider = inferProvider(req.Model)

	// Store in cache
	if err := c.cache.Set(cacheEntry); err != nil {
		// Log cache error but don't fail the request
		fmt.Printf("Warning: failed to cache response: %v\n", err)
	}

	return resp, nil
}

// inferProvider infers the provider from model name
func inferProvider(model string) string {
	if len(model) >= 4 && model[:4] == "gpt-" {
		return "openai"
	}
	if len(model) >= 7 && model[:7] == "claude-" {
		return "anthropic"
	}
	if len(model) >= 7 && model[:7] == "gemini-" {
		return "gemini"
	}
	if len(model) >= 3 && model[:3] == "o1-" {
		return "openai"
	}
	return "unknown"
}

// WithCache returns a middleware option that adds caching capabilities
func WithCache(config ...CacheConfig) Option {
	return func(p Provider) Provider {
		provider, err := newCachingProvider(p, config...)
		if err != nil {
			// If cache creation fails, return the original provider
			logger.Errorf("Warning: failed to create caching middleware: %v", err)
			return p
		}
		return provider
	}
}

// WithCacheInstance is a convenience function for creating cache middleware with just a cache instance
func WithCacheInstance(cache *cache.Cache) Option {
	return WithCache(CacheConfig{
		Cache: cache,
	})
}
