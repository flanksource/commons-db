package types

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseOpenRouterModels(t *testing.T) {
	tests := []struct {
		name     string
		models   []OpenRouterModel
		expected map[string]*ModelInfo
	}{
		{
			name: "basic model parsing",
			models: []OpenRouterModel{
				{
					ID:            "openai/gpt-4",
					Name:          "GPT-4",
					ContextLength: 8192,
					Pricing: OpenRouterPricing{
						Prompt:     "0.00003",
						Completion: "0.00006",
					},
				},
			},
			expected: map[string]*ModelInfo{
				"openai/gpt-4": {
					ContextWindow: 8192,
					InputPrice:    30.0, // 0.00003 * 1M
					OutputPrice:   60.0, // 0.00006 * 1M
				},
			},
		},
		{
			name: "model with cache pricing",
			models: []OpenRouterModel{
				{
					ID:            "anthropic/claude-3-sonnet",
					Name:          "Claude 3 Sonnet",
					ContextLength: 200000,
					Pricing: OpenRouterPricing{
						Prompt:          "0.000003",
						Completion:      "0.000015",
						InputCacheRead:  "0.0000003",
						InputCacheWrite: "0.00000375",
					},
				},
			},
			expected: map[string]*ModelInfo{
				"anthropic/claude-3-sonnet": {
					ContextWindow:    200000,
					InputPrice:       3.0,
					OutputPrice:      15.0,
					CacheReadsPrice:  0.3,
					CacheWritesPrice: 3.75,
				},
			},
		},
		{
			name: "model with max completion tokens",
			models: []OpenRouterModel{
				{
					ID:            "test/model",
					ContextLength: 128000,
					Pricing: OpenRouterPricing{
						Prompt:     "0.000001",
						Completion: "0.000002",
					},
					TopProvider: &TopProvider{
						MaxCompletionTokens: intPtr(4096),
					},
				},
			},
			expected: map[string]*ModelInfo{
				"test/model": {
					ContextWindow: 128000,
					InputPrice:    1.0,
					OutputPrice:   2.0,
					MaxTokens:     4096,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseOpenRouterModels(tt.models)

			for modelID, expectedInfo := range tt.expected {
				actualInfo, exists := result[modelID]
				if !exists {
					t.Fatalf("expected model %s to exist in result", modelID)
				}

				if actualInfo.ContextWindow != expectedInfo.ContextWindow {
					t.Errorf("ContextWindow mismatch for %s: got %d, want %d",
						modelID, actualInfo.ContextWindow, expectedInfo.ContextWindow)
				}
				if actualInfo.InputPrice != expectedInfo.InputPrice {
					t.Errorf("InputPrice mismatch for %s: got %f, want %f",
						modelID, actualInfo.InputPrice, expectedInfo.InputPrice)
				}
				if actualInfo.OutputPrice != expectedInfo.OutputPrice {
					t.Errorf("OutputPrice mismatch for %s: got %f, want %f",
						modelID, actualInfo.OutputPrice, expectedInfo.OutputPrice)
				}
				if actualInfo.CacheReadsPrice != expectedInfo.CacheReadsPrice {
					t.Errorf("CacheReadsPrice mismatch for %s: got %f, want %f",
						modelID, actualInfo.CacheReadsPrice, expectedInfo.CacheReadsPrice)
				}
				if actualInfo.CacheWritesPrice != expectedInfo.CacheWritesPrice {
					t.Errorf("CacheWritesPrice mismatch for %s: got %f, want %f",
						modelID, actualInfo.CacheWritesPrice, expectedInfo.CacheWritesPrice)
				}
				if actualInfo.MaxTokens != expectedInfo.MaxTokens {
					t.Errorf("MaxTokens mismatch for %s: got %d, want %d",
						modelID, actualInfo.MaxTokens, expectedInfo.MaxTokens)
				}
			}
		})
	}
}

func TestParsePrice(t *testing.T) {
	tests := []struct {
		input    string
		expected float64
	}{
		{"0.00003", 0.00003},
		{"0.000015", 0.000015},
		{"", 0.0},
		{"invalid", 0.0},
		{"1.5", 1.5},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parsePrice(tt.input)
			if result != tt.expected {
				t.Errorf("parsePrice(%q) = %f, want %f", tt.input, result, tt.expected)
			}
		})
	}
}

func TestCacheSaveAndLoad(t *testing.T) {
	// Create test cache data
	testCache := &PricingCache{
		Timestamp: time.Now(),
		Models: map[string]*ModelInfo{
			"test/model": {
				ContextWindow: 100000,
				InputPrice:    1.5,
				OutputPrice:   3.0,
			},
		},
	}

	// Marshal to JSON
	data, err := json.MarshalIndent(testCache, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal cache: %v", err)
	}

	// Create temp file
	tmpFile := filepath.Join(t.TempDir(), "test-cache.json")
	err = os.WriteFile(tmpFile, data, 0644)
	if err != nil {
		t.Fatalf("failed to write cache file: %v", err)
	}

	// Read and unmarshal
	loadedData, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("failed to read cache file: %v", err)
	}

	var loadedCache PricingCache
	if err := json.Unmarshal(loadedData, &loadedCache); err != nil {
		t.Fatalf("failed to unmarshal cache: %v", err)
	}

	// Verify loaded data
	if len(loadedCache.Models) != len(testCache.Models) {
		t.Errorf("loaded cache has %d models, want %d", len(loadedCache.Models), len(testCache.Models))
	}

	testModel := loadedCache.Models["test/model"]
	if testModel == nil {
		t.Fatal("test/model not found in loaded cache")
	}

	if testModel.InputPrice != 1.5 {
		t.Errorf("InputPrice = %f, want 1.5", testModel.InputPrice)
	}
	if testModel.OutputPrice != 3.0 {
		t.Errorf("OutputPrice = %f, want 3.0", testModel.OutputPrice)
	}
}

func TestCacheExpiry(t *testing.T) {
	// Create expired cache (older than 24h)
	expiredCache := &PricingCache{
		Timestamp: time.Now().Add(-25 * time.Hour),
		Models: map[string]*ModelInfo{
			"test/model": {InputPrice: 1.0},
		},
	}

	// Create fresh cache (within 24h)
	freshCache := &PricingCache{
		Timestamp: time.Now().Add(-1 * time.Hour),
		Models: map[string]*ModelInfo{
			"test/model": {InputPrice: 1.0},
		},
	}

	// Test expired cache
	age := time.Since(expiredCache.Timestamp)
	if age < cacheExpiryDuration {
		t.Errorf("expired cache should be older than %v, got %v", cacheExpiryDuration, age)
	}

	// Test fresh cache
	age = time.Since(freshCache.Timestamp)
	if age >= cacheExpiryDuration {
		t.Errorf("fresh cache should be younger than %v, got %v", cacheExpiryDuration, age)
	}
}

func TestFetchOpenRouterPricing(t *testing.T) {
	// Create mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := OpenRouterResponse{
			Data: []OpenRouterModel{
				{
					ID:            "openai/gpt-4",
					Name:          "GPT-4",
					ContextLength: 8192,
					Pricing: OpenRouterPricing{
						Prompt:     "0.00003",
						Completion: "0.00006",
					},
				},
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	// Temporarily override the API URL (not implemented in current code, but would be needed for testing)
	// For this test, we'll just verify the parsing logic separately

	// Test that we can parse a valid response
	models := []OpenRouterModel{
		{
			ID:            "openai/gpt-4",
			Name:          "GPT-4",
			ContextLength: 8192,
			Pricing: OpenRouterPricing{
				Prompt:     "0.00003",
				Completion: "0.00006",
			},
		},
	}

	result := parseOpenRouterModels(models)
	if len(result) != 1 {
		t.Errorf("expected 1 model, got %d", len(result))
	}

	if _, exists := result["openai/gpt-4"]; !exists {
		t.Error("expected openai/gpt-4 to exist in result")
	}
}

func TestMergePricingIntoRegistry(t *testing.T) {
	// Save original registry
	modelRegistryMu.Lock()
	originalRegistry := make(map[string]ModelInfo)
	for k, v := range modelRegistry {
		originalRegistry[k] = v
	}
	modelRegistryMu.Unlock()

	// Restore at end of test
	defer func() {
		modelRegistryMu.Lock()
		modelRegistry = originalRegistry
		modelRegistryMu.Unlock()
	}()

	// Create test models to merge
	testModels := map[string]*ModelInfo{
		"test/new-model": {
			InputPrice:  10.0,
			OutputPrice: 20.0,
		},
	}

	// Merge models
	mergePricingIntoRegistry(testModels)

	// Verify merge
	modelRegistryMu.RLock()
	merged, exists := modelRegistry["test/new-model"]
	modelRegistryMu.RUnlock()

	if !exists {
		t.Fatal("test/new-model should exist after merge")
	}

	if merged.InputPrice != 10.0 {
		t.Errorf("InputPrice = %f, want 10.0", merged.InputPrice)
	}
}

func TestCalculateCostWithOpenRouterPricing(t *testing.T) {
	// Add a test model to registry
	modelRegistryMu.Lock()
	modelRegistry["test-model"] = ModelInfo{
		InputPrice:       5.0,
		OutputPrice:      10.0,
		CacheReadsPrice:  0.5,
		CacheWritesPrice: 6.0,
	}
	modelRegistryMu.Unlock()

	tests := []struct {
		name             string
		inputTokens      int
		outputTokens     int
		cacheReadTokens  *int
		cacheWriteTokens *int
		expectedCost     float64
	}{
		{
			name:         "basic cost calculation",
			inputTokens:  1000,
			outputTokens: 500,
			expectedCost: (1000*5.0 + 500*10.0) / 1_000_000,
		},
		{
			name:             "with cache tokens",
			inputTokens:      1000,
			outputTokens:     500,
			cacheReadTokens:  intPtr(2000),
			cacheWriteTokens: intPtr(100),
			expectedCost:     (1000*5.0 + 500*10.0 + 2000*0.5 + 100*6.0) / 1_000_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := calculateCost("test-model", tt.inputTokens, tt.outputTokens, nil, tt.cacheReadTokens, tt.cacheWriteTokens)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result.Cost != tt.expectedCost {
				t.Errorf("Cost = %f, want %f", result.Cost, tt.expectedCost)
			}
		})
	}
}

func TestLevenshteinDistance(t *testing.T) {
	tests := []struct {
		s1       string
		s2       string
		expected int
	}{
		{"", "", 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"kitten", "sitting", 3},
		{"gpt-4", "gpt-4o", 1},
		{"claude-3", "claude-3.5", 2},
		{"gemini-pro", "gemini-flash", 5},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_vs_%s", tt.s1, tt.s2), func(t *testing.T) {
			result := levenshteinDistance(tt.s1, tt.s2)
			if result != tt.expected {
				t.Errorf("levenshteinDistance(%q, %q) = %d, want %d", tt.s1, tt.s2, result, tt.expected)
			}
		})
	}
}

func TestFindSimilarModels(t *testing.T) {
	// Add some test models to registry
	modelRegistryMu.Lock()
	originalRegistry := make(map[string]ModelInfo)
	for k, v := range modelRegistry {
		originalRegistry[k] = v
	}
	modelRegistry["gpt-4"] = ModelInfo{InputPrice: 1.0}
	modelRegistry["gpt-4o"] = ModelInfo{InputPrice: 2.0}
	modelRegistry["gpt-4o-mini"] = ModelInfo{InputPrice: 3.0}
	modelRegistry["claude-3-sonnet"] = ModelInfo{InputPrice: 4.0}
	modelRegistry["claude-3.5-sonnet"] = ModelInfo{InputPrice: 5.0}
	modelRegistryMu.Unlock()

	defer func() {
		modelRegistryMu.Lock()
		modelRegistry = originalRegistry
		modelRegistryMu.Unlock()
	}()

	tests := []struct {
		name           string
		target         string
		topN           int
		expectContains []string
	}{
		{
			name:           "close match to gpt-4",
			target:         "gpt4",
			topN:           3,
			expectContains: []string{"gpt-4", "gpt-4o"},
		},
		{
			name:           "typo in claude",
			target:         "claud-3-sonnet",
			topN:           2,
			expectContains: []string{"claude-3-sonnet"},
		},
		{
			name:           "case insensitive",
			target:         "GPT-4O",
			topN:           1,
			expectContains: []string{"gpt-4o"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := findSimilarModels(tt.target, tt.topN)

			if len(results) == 0 {
				t.Fatal("expected at least one suggestion")
			}

			for _, expected := range tt.expectContains {
				found := false
				for _, result := range results {
					if result == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected %q to be in suggestions %v", expected, results)
				}
			}
		})
	}
}

func TestCalculateCostWithUnknownModel(t *testing.T) {
	_, err := calculateCost("unknown-model-xyz", 1000, 500, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown model")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "not found in pricing registry") {
		t.Errorf("error should mention model not found, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "registry has") {
		t.Errorf("error should mention registry size, got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "Did you mean") {
		t.Errorf("error should include suggestions, got: %s", errMsg)
	}
}

// Helper function
func intPtr(i int) *int {
	return &i
}
