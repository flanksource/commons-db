package types

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ModelInfo contains pricing and capability information for a specific LLM model.
type ModelInfo struct {
	Provider            LLMBackend
	ModelID             string
	MaxTokens           int
	ContextWindow       int
	SupportsImages      bool
	SupportsPromptCache bool
	InputPrice          float64 // Per million tokens
	OutputPrice         float64 // Per million tokens
	InputPriceTiers     []PriceTier
	OutputPriceTiers    []PriceTier
	CacheWritesPrice    float64 // Per million tokens
	CacheReadsPrice     float64 // Per million tokens
}

// PriceTier represents a pricing tier for models with tiered pricing.
type PriceTier struct {
	TokenLimit int     // Upper limit (inclusive)
	Price      float64 // Per million tokens
}

// CalculateCost calculates the cost of an LLM request based on token usage and model pricing.
func CalculateCost(model string, inputTokens, outputTokens int, reasoningTokens, cacheReadTokens, cacheWriteTokens *int) (CostInfo, error) {
	return calculateCost(model, inputTokens, outputTokens, reasoningTokens, cacheReadTokens, cacheWriteTokens)
}

// calculateCost is the internal cost calculation function
func calculateCost(model string, inputTokens, outputTokens int, reasoningTokens, cacheReadTokens, cacheWriteTokens *int) (CostInfo, error) {
	// Ensure OpenRouter pricing is loaded
	if _, err := NewPricingCache(); err != nil {
		return CostInfo{}, err
	}

	costInfo := CostInfo{
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		ReasoningTokens:  reasoningTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
		Model:            model,
	}

	// Look up model in registry
	modelRegistryMu.RLock()
	modelInfo, exists := modelRegistry[model]
	registrySize := len(modelRegistry)
	modelRegistryMu.RUnlock()

	if !exists {
		suggestions := findSimilarModels(model, 5)
		var suggestionMsg string
		if len(suggestions) > 0 {
			suggestionMsg = fmt.Sprintf(". Did you mean: %s", strings.Join(suggestions, ", "))
		}
		return costInfo, fmt.Errorf("%s not found in pricing registry (registry has %d models): %s",
			model, registrySize, suggestionMsg)
	}

	var totalCost float64

	// Calculate input token cost (with tiers if applicable)
	if len(modelInfo.InputPriceTiers) > 0 {
		totalCost += calculateTieredCost(inputTokens, modelInfo.InputPriceTiers)
	} else {
		totalCost += float64(inputTokens) * modelInfo.InputPrice / 1_000_000
	}

	// Calculate output token cost (with tiers if applicable)
	if len(modelInfo.OutputPriceTiers) > 0 {
		totalCost += calculateTieredCost(outputTokens, modelInfo.OutputPriceTiers)
	} else {
		totalCost += float64(outputTokens) * modelInfo.OutputPrice / 1_000_000
	}

	// Calculate reasoning token cost (if applicable, e.g., OpenAI o1 models)
	if reasoningTokens != nil && *reasoningTokens > 0 {
		// Reasoning tokens typically use output token pricing
		totalCost += float64(*reasoningTokens) * modelInfo.OutputPrice / 1_000_000
	}

	// Calculate cache read cost (if applicable, e.g., Anthropic)
	if cacheReadTokens != nil && *cacheReadTokens > 0 && modelInfo.CacheReadsPrice > 0 {
		totalCost += float64(*cacheReadTokens) * modelInfo.CacheReadsPrice / 1_000_000
	}

	// Calculate cache write cost (if applicable, e.g., Anthropic)
	if cacheWriteTokens != nil && *cacheWriteTokens > 0 && modelInfo.CacheWritesPrice > 0 {
		totalCost += float64(*cacheWriteTokens) * modelInfo.CacheWritesPrice / 1_000_000
	}

	costInfo.Cost = totalCost
	return costInfo, nil
}

// calculateTieredCost calculates cost based on tiered pricing.
func calculateTieredCost(tokens int, tiers []PriceTier) float64 {
	var cost float64
	remainingTokens := tokens

	for _, tier := range tiers {
		if remainingTokens <= 0 {
			break
		}

		tokensInTier := tier.TokenLimit
		if tokensInTier > remainingTokens {
			tokensInTier = remainingTokens
		}

		cost += float64(tokensInTier) * tier.Price / 1_000_000
		remainingTokens -= tokensInTier
	}

	// If tokens exceed all tiers, use last tier's price
	if remainingTokens > 0 && len(tiers) > 0 {
		lastTier := tiers[len(tiers)-1]
		cost += float64(remainingTokens) * lastTier.Price / 1_000_000
	}

	return cost
}

// modelRegistry contains pricing information for all supported models.
var (
	modelRegistry   = map[string]ModelInfo{}
	modelRegistryMu sync.RWMutex
)

// GetModelInfo retrieves pricing information for a specific model from the registry.
// Returns the ModelInfo and a boolean indicating whether the model was found.
func GetModelInfo(model string) (ModelInfo, bool) {
	// Ensure OpenRouter pricing is loaded
	NewPricingCache()

	modelRegistryMu.RLock()
	defer modelRegistryMu.RUnlock()

	info, exists := modelRegistry[model]
	return info, exists
}

// levenshteinDistance calculates the Levenshtein distance between two strings
func levenshteinDistance(s1, s2 string) int {
	if len(s1) == 0 {
		return len(s2)
	}
	if len(s2) == 0 {
		return len(s1)
	}

	// Create matrix
	matrix := make([][]int, len(s1)+1)
	for i := range matrix {
		matrix[i] = make([]int, len(s2)+1)
	}

	// Initialize first row and column
	for i := 0; i <= len(s1); i++ {
		matrix[i][0] = i
	}
	for j := 0; j <= len(s2); j++ {
		matrix[0][j] = j
	}

	// Fill matrix
	for i := 1; i <= len(s1); i++ {
		for j := 1; j <= len(s2); j++ {
			cost := 1
			if s1[i-1] == s2[j-1] {
				cost = 0
			}

			matrix[i][j] = min(
				matrix[i-1][j]+1,      // deletion
				matrix[i][j-1]+1,      // insertion
				matrix[i-1][j-1]+cost, // substitution
			)
		}
	}

	return matrix[len(s1)][len(s2)]
}

// min returns the minimum of three integers
func min(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

// modelMatch holds a model name and its similarity score
type modelMatch struct {
	name     string
	distance int
}

// findSimilarModels finds the top N most similar model names using Levenshtein distance
func findSimilarModels(target string, topN int) []string {
	modelRegistryMu.RLock()
	defer modelRegistryMu.RUnlock()

	if len(modelRegistry) == 0 {
		return nil
	}

	matches := make([]modelMatch, 0, len(modelRegistry))
	targetLower := strings.ToLower(target)

	for modelName := range modelRegistry {
		modelLower := strings.ToLower(modelName)
		distance := levenshteinDistance(targetLower, modelLower)
		matches = append(matches, modelMatch{
			name:     modelName,
			distance: distance,
		})
	}

	// Sort by distance (ascending)
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].distance < matches[j].distance
	})

	// Return top N matches
	n := topN
	if n > len(matches) {
		n = len(matches)
	}

	result := make([]string, n)
	for i := 0; i < n; i++ {
		result[i] = matches[i].name
	}

	return result
}
