package middleware

import (
	"errors"
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

	logger.Debugf("Configuring cache with ttl=%v for %v", config[0].Cache.GetTTL(), provider)

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

// GetOpenRouterModelID returns the OpenRouter model identifier from the wrapped provider
func (c *cachingProvider) GetOpenRouterModelID() string {
	return c.provider.GetOpenRouterModelID()
}

// Execute implements the Provider interface with caching
func (c *cachingProvider) Execute(sess *Session, req ProviderRequest) (ProviderResponse, error) {
	// Check if cache should be bypassed
	if shouldBypassCache(sess.Context) {
		return c.provider.Execute(sess, req)
	}

	// Try to get from cache
	cachedEntry, err := c.cache.Get(req.Prompt, req.Model)
	if err == nil && cachedEntry != nil && cachedEntry.Error == "" {
		logger.Infof("[%s] cache hit %v", c.GetOpenRouterModelID(), cachedEntry.CostUSD)
		// Cache hit - return cached response
		resp := ProviderResponse{
			Cached: true,
			Text:   cachedEntry.Response,
			Model:  cachedEntry.Model,
		}

		return resp, nil
	} else if err != nil && !errors.Is(err, cache.ErrNotFound) && !errors.Is(err, cache.ErrCacheDisabled) {
		// Cache error
		return ProviderResponse{}, fmt.Errorf("failed to get cache: %w", err)
	}
	logger.Infof("[%s] cache miss", req.Model)

	// Cache miss - execute request
	startTime := time.Now()
	startCost := sess.Costs.Sum().TotalCost()
	resp, execErr := c.provider.Execute(sess, req)
	duration := time.Since(startTime)

	// Prepare cache entry
	cacheEntry := &cache.Entry{
		Model:      req.Model,
		Prompt:     req.Prompt,
		CostUSD:    sess.Costs.Sum().TotalCost() - startCost,
		DurationMS: int64(duration.Milliseconds()),
	}

	if execErr != nil {
		// Cache the error
		cacheEntry.Error = execErr.Error()
		if err = c.cache.Set(cacheEntry); err != nil {
			return resp, fmt.Errorf("failed to set cache: %w", err)
		}

		return resp, execErr
	}

	// Store in cache
	if err := c.cache.Set(cacheEntry); err != nil {
		return resp, fmt.Errorf("failed to set cache: %w", err)

	}

	return resp, nil
}

// WithCache returns a middleware option that adds caching capabilities
func WithCache(config ...CacheConfig) Option {
	return func(p Provider) (Provider, error) {
		provider, err := newCachingProvider(p, config...)
		if err != nil {
			return p, err
		}
		return provider, nil
	}
}

// WithCacheInstance is a convenience function for creating cache middleware with just a cache instance
func WithCacheInstance(cache *cache.Cache) Option {
	return WithCache(CacheConfig{
		Cache: cache,
	})
}
