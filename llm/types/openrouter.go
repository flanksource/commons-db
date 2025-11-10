package types

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/flanksource/commons/logger"
)

const (
	openRouterAPIURL    = "https://openrouter.ai/api/v1/models"
	cacheExpiryDuration = 24 * time.Hour
	cacheDirName        = "flanksource"
	cacheFileName       = "openrouter-pricing.json"
)

var (
	cachedPricing     *PricingCache
	cachedPricingErr  error
	cachedPricingLock sync.Mutex
)

// OpenRouterResponse represents the response from OpenRouter API
type OpenRouterResponse struct {
	Data []OpenRouterModel `json:"data"`
}

// OpenRouterModel represents a single model from OpenRouter API
type OpenRouterModel struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Pricing       OpenRouterPricing `json:"pricing"`
	ContextLength int               `json:"context_length"`
	TopProvider   *TopProvider      `json:"top_provider,omitempty"`
}

// OpenRouterPricing contains pricing information for a model
type OpenRouterPricing struct {
	Prompt            string `json:"prompt"`                       // Per-token input price
	Completion        string `json:"completion"`                   // Per-token output price
	Request           string `json:"request,omitempty"`            // Per-request fee
	Image             string `json:"image,omitempty"`              // Image processing cost
	InternalReasoning string `json:"internal_reasoning,omitempty"` // Reasoning token cost
	InputCacheRead    string `json:"input_cache_read,omitempty"`   // Cache read cost
	InputCacheWrite   string `json:"input_cache_write,omitempty"`  // Cache write cost
}

// TopProvider contains provider-specific limits
type TopProvider struct {
	MaxCompletionTokens *int `json:"max_completion_tokens,omitempty"`
}

// PricingCache represents the cached pricing data
type PricingCache struct {
	Timestamp time.Time             `json:"timestamp"`
	Models    map[string]*ModelInfo `json:"models"`
}

// NewPricingCache loads or fetches pricing cache, handling the full lifecycle:
// memory cache → disk cache → API fetch. Automatically merges into model registry.
func NewPricingCache() (*PricingCache, error) {
	cachedPricingLock.Lock()
	defer cachedPricingLock.Unlock()

	// Return cached error if we already tried and failed
	if cachedPricingErr != nil {
		return nil, cachedPricingErr
	}

	// If we have a valid in-memory cache, return it
	if cachedPricing != nil && !cachedPricing.IsExpired() {
		return cachedPricing, nil
	}

	// Try to load from disk cache
	if cachedPricing == nil {
		cache, err := loadFromDisk()
		if err == nil && cache != nil && !cache.IsExpired() {
			logger.Debugf("Loading OpenRouter pricing from cache (age: %s)", time.Since(cache.Timestamp))
			cachedPricing = cache
			mergePricingIntoRegistry(cache.Models)
			return cachedPricing, nil
		}

		logger.Debugf("Fetching fresh OpenRouter pricing (cache age: %v, error: %v)",
			func() string {
				if cache != nil {
					return time.Since(cache.Timestamp).String()
				}
				return "no cache"
			}(), err)
	}

	// Fetch fresh pricing from API
	models, err := fetchAndCacheOpenRouterPricing()
	if err != nil {
		logger.Warnf("Failed to fetch OpenRouter pricing: %v. Using hardcoded pricing.", err)
		cachedPricingErr = err
		return nil, err
	}

	cachedPricing = &PricingCache{
		Timestamp: time.Now(),
		Models:    models,
	}
	mergePricingIntoRegistry(models)
	return cachedPricing, nil
}

// loadFromDisk loads pricing cache from disk file
func loadFromDisk() (*PricingCache, error) {
	cachePath, err := getCachePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // Cache doesn't exist, not an error
		}
		return nil, fmt.Errorf("failed to read cache file: %w", err)
	}

	var cache PricingCache
	if err := json.Unmarshal(data, &cache); err != nil {
		// Cache corrupted, delete it
		os.Remove(cachePath)
		return nil, fmt.Errorf("failed to parse cache file: %w", err)
	}

	return &cache, nil
}

// Save writes the pricing cache to disk
func (c *PricingCache) Save() error {
	cachePath, err := getCachePath()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal cache: %w", err)
	}

	// Write atomically using temp file + rename
	tmpPath := cachePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	if err := os.Rename(tmpPath, cachePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename cache file: %w", err)
	}

	return nil
}

// IsExpired returns true if the cache is older than the expiry duration
func (c *PricingCache) IsExpired() bool {
	return time.Since(c.Timestamp) >= cacheExpiryDuration
}

// fetchAndCacheOpenRouterPricing fetches pricing from OpenRouter API and caches it
func fetchAndCacheOpenRouterPricing() (map[string]*ModelInfo, error) {
	resp, err := http.Get(openRouterAPIURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch OpenRouter pricing: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenRouter API returned status %d", resp.StatusCode)
	}

	var apiResponse OpenRouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResponse); err != nil {
		return nil, fmt.Errorf("failed to decode OpenRouter response: %w", err)
	}

	models := parseOpenRouterModels(apiResponse.Data)

	// Save to cache
	cache := &PricingCache{
		Timestamp: time.Now(),
		Models:    models,
	}
	if err := cache.Save(); err != nil {
		logger.Warnf("Failed to save OpenRouter pricing cache: %v", err)
	}

	return models, nil
}

// parseOpenRouterModels converts OpenRouter API models to ModelInfo map
func parseOpenRouterModels(models []OpenRouterModel) map[string]*ModelInfo {
	result := make(map[string]*ModelInfo, len(models))

	for _, model := range models {
		info := &ModelInfo{
			ContextWindow: model.ContextLength,
		}

		// Convert per-token prices to per-million-tokens
		if inputPrice := parsePrice(model.Pricing.Prompt); inputPrice > 0 {
			info.InputPrice = inputPrice * 1_000_000
		}
		if outputPrice := parsePrice(model.Pricing.Completion); outputPrice > 0 {
			info.OutputPrice = outputPrice * 1_000_000
		}
		if cacheReadPrice := parsePrice(model.Pricing.InputCacheRead); cacheReadPrice > 0 {
			info.CacheReadsPrice = cacheReadPrice * 1_000_000
		}
		if cacheWritePrice := parsePrice(model.Pricing.InputCacheWrite); cacheWritePrice > 0 {
			info.CacheWritesPrice = cacheWritePrice * 1_000_000
		}

		// Set max tokens from provider info
		if model.TopProvider != nil && model.TopProvider.MaxCompletionTokens != nil {
			info.MaxTokens = *model.TopProvider.MaxCompletionTokens
		}

		result[model.ID] = info
	}

	return result
}

// parsePrice converts string price to float64
func parsePrice(priceStr string) float64 {
	if priceStr == "" {
		return 0
	}
	var price float64
	fmt.Sscanf(priceStr, "%f", &price)
	return price
}

// mergePricingIntoRegistry merges OpenRouter pricing into the global model registry
func mergePricingIntoRegistry(models map[string]*ModelInfo) {
	modelRegistryMu.Lock()
	defer modelRegistryMu.Unlock()

	for modelID, info := range models {
		modelRegistry[modelID] = *info
	}
	logger.Debugf("Merged %d OpenRouter models into pricing registry", len(models))
}

// getCachePath returns the path to the pricing cache file
func getCachePath() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user cache directory: %w", err)
	}

	dir := filepath.Join(cacheDir, cacheDirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create cache directory: %w", err)
	}

	return filepath.Join(dir, cacheFileName), nil
}
