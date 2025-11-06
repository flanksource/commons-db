package llm

import "fmt"

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

// calculateCost calculates the cost of an LLM request based on token usage and model pricing.
func calculateCost(model string, inputTokens, outputTokens int, reasoningTokens, cacheReadTokens, cacheWriteTokens *int) (CostInfo, error) {
	costInfo := CostInfo{
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		ReasoningTokens:  reasoningTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
		Model:            model,
	}

	// Look up model in registry
	modelInfo, exists := modelRegistry[model]
	if !exists {
		return costInfo, fmt.Errorf("model not found in pricing registry: %s", model)
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
var modelRegistry = map[string]ModelInfo{
	// OpenAI GPT-4o models
	"gpt-4o": {
		Provider:       LLMBackendOpenAI,
		ModelID:        "gpt-4o",
		MaxTokens:      16384,
		ContextWindow:  128000,
		SupportsImages: true,
		InputPrice:     2.50,
		OutputPrice:    10.00,
	},
	"gpt-4o-2024-11-20": {
		Provider:       LLMBackendOpenAI,
		ModelID:        "gpt-4o-2024-11-20",
		MaxTokens:      16384,
		ContextWindow:  128000,
		SupportsImages: true,
		InputPrice:     2.50,
		OutputPrice:    10.00,
	},
	"gpt-4o-mini": {
		Provider:       LLMBackendOpenAI,
		ModelID:        "gpt-4o-mini",
		MaxTokens:      16384,
		ContextWindow:  128000,
		SupportsImages: true,
		InputPrice:     0.150,
		OutputPrice:    0.600,
	},

	// OpenAI o1 models (with reasoning tokens)
	"o1": {
		Provider:       LLMBackendOpenAI,
		ModelID:        "o1",
		MaxTokens:      100000,
		ContextWindow:  200000,
		SupportsImages: true,
		InputPrice:     15.00,
		OutputPrice:    60.00,
	},
	"o1-mini": {
		Provider:       LLMBackendOpenAI,
		ModelID:        "o1-mini",
		MaxTokens:      65536,
		ContextWindow:  128000,
		SupportsImages: true,
		InputPrice:     3.00,
		OutputPrice:    12.00,
	},

	// OpenAI GPT-4 Turbo
	"gpt-4-turbo": {
		Provider:       LLMBackendOpenAI,
		ModelID:        "gpt-4-turbo",
		MaxTokens:      4096,
		ContextWindow:  128000,
		SupportsImages: true,
		InputPrice:     10.00,
		OutputPrice:    30.00,
	},

	// OpenAI GPT-3.5 Turbo
	"gpt-3.5-turbo": {
		Provider:      LLMBackendOpenAI,
		ModelID:       "gpt-3.5-turbo",
		MaxTokens:     4096,
		ContextWindow: 16385,
		InputPrice:    0.50,
		OutputPrice:   1.50,
	},

	// Anthropic Claude 3.7 Sonnet
	"claude-3.7-sonnet": {
		Provider:            LLMBackendAnthropic,
		ModelID:             "claude-3-7-sonnet-20250219",
		MaxTokens:           8192,
		ContextWindow:       200000,
		SupportsImages:      true,
		SupportsPromptCache: true,
		InputPrice:          3.00,
		OutputPrice:         15.00,
		CacheWritesPrice:    3.75,
		CacheReadsPrice:     0.30,
	},
	"claude-3-7-sonnet-20250219": {
		Provider:            LLMBackendAnthropic,
		ModelID:             "claude-3-7-sonnet-20250219",
		MaxTokens:           8192,
		ContextWindow:       200000,
		SupportsImages:      true,
		SupportsPromptCache: true,
		InputPrice:          3.00,
		OutputPrice:         15.00,
		CacheWritesPrice:    3.75,
		CacheReadsPrice:     0.30,
	},

	// Anthropic Claude 3.5 Sonnet
	"claude-3.5-sonnet": {
		Provider:            LLMBackendAnthropic,
		ModelID:             "claude-3-5-sonnet-20241022",
		MaxTokens:           8192,
		ContextWindow:       200000,
		SupportsImages:      true,
		SupportsPromptCache: true,
		InputPrice:          3.00,
		OutputPrice:         15.00,
		CacheWritesPrice:    3.75,
		CacheReadsPrice:     0.30,
	},
	"claude-3-5-sonnet-20241022": {
		Provider:            LLMBackendAnthropic,
		ModelID:             "claude-3-5-sonnet-20241022",
		MaxTokens:           8192,
		ContextWindow:       200000,
		SupportsImages:      true,
		SupportsPromptCache: true,
		InputPrice:          3.00,
		OutputPrice:         15.00,
		CacheWritesPrice:    3.75,
		CacheReadsPrice:     0.30,
	},

	// Anthropic Claude 3 Opus
	"claude-3-opus": {
		Provider:            LLMBackendAnthropic,
		ModelID:             "claude-3-opus-20240229",
		MaxTokens:           4096,
		ContextWindow:       200000,
		SupportsImages:      true,
		SupportsPromptCache: true,
		InputPrice:          15.00,
		OutputPrice:         75.00,
		CacheWritesPrice:    18.75,
		CacheReadsPrice:     1.50,
	},
	"claude-3-opus-20240229": {
		Provider:            LLMBackendAnthropic,
		ModelID:             "claude-3-opus-20240229",
		MaxTokens:           4096,
		ContextWindow:       200000,
		SupportsImages:      true,
		SupportsPromptCache: true,
		InputPrice:          15.00,
		OutputPrice:         75.00,
		CacheWritesPrice:    18.75,
		CacheReadsPrice:     1.50,
	},

	// Anthropic Claude 3.5 Haiku
	"claude-3.5-haiku": {
		Provider:            LLMBackendAnthropic,
		ModelID:             "claude-3-5-haiku-20241022",
		MaxTokens:           8192,
		ContextWindow:       200000,
		SupportsImages:      false,
		SupportsPromptCache: true,
		InputPrice:          0.80,
		OutputPrice:         4.00,
		CacheWritesPrice:    1.00,
		CacheReadsPrice:     0.08,
	},
	"claude-3-5-haiku-20241022": {
		Provider:            LLMBackendAnthropic,
		ModelID:             "claude-3-5-haiku-20241022",
		MaxTokens:           8192,
		ContextWindow:       200000,
		SupportsImages:      false,
		SupportsPromptCache: true,
		InputPrice:          0.80,
		OutputPrice:         4.00,
		CacheWritesPrice:    1.00,
		CacheReadsPrice:     0.08,
	},

	// Google Gemini 2.5 Pro
	"gemini-2.5-pro": {
		Provider:       LLMBackendGemini,
		ModelID:        "gemini-2.5-pro-exp-03-25",
		MaxTokens:      8192,
		ContextWindow:  1000000,
		SupportsImages: true,
		InputPriceTiers: []PriceTier{
			{TokenLimit: 128000, Price: 1.25},
			{TokenLimit: 1000000, Price: 2.50},
		},
		OutputPrice: 10.00,
	},
	"gemini-2.5-pro-exp-03-25": {
		Provider:       LLMBackendGemini,
		ModelID:        "gemini-2.5-pro-exp-03-25",
		MaxTokens:      8192,
		ContextWindow:  1000000,
		SupportsImages: true,
		InputPriceTiers: []PriceTier{
			{TokenLimit: 128000, Price: 1.25},
			{TokenLimit: 1000000, Price: 2.50},
		},
		OutputPrice: 10.00,
	},

	// Google Gemini 2.0 Flash
	"gemini-2.0-flash": {
		Provider:       LLMBackendGemini,
		ModelID:        "gemini-2.0-flash-exp",
		MaxTokens:      8192,
		ContextWindow:  1000000,
		SupportsImages: true,
		InputPriceTiers: []PriceTier{
			{TokenLimit: 128000, Price: 0.00},
			{TokenLimit: 1000000, Price: 0.00},
		},
		OutputPrice: 0.00,
	},
	"gemini-2.0-flash-exp": {
		Provider:       LLMBackendGemini,
		ModelID:        "gemini-2.0-flash-exp",
		MaxTokens:      8192,
		ContextWindow:  1000000,
		SupportsImages: true,
		InputPriceTiers: []PriceTier{
			{TokenLimit: 128000, Price: 0.00},
			{TokenLimit: 1000000, Price: 0.00},
		},
		OutputPrice: 0.00,
	},

	// Google Gemini 1.5 Pro
	"gemini-1.5-pro": {
		Provider:       LLMBackendGemini,
		ModelID:        "gemini-1.5-pro-002",
		MaxTokens:      8192,
		ContextWindow:  2000000,
		SupportsImages: true,
		InputPriceTiers: []PriceTier{
			{TokenLimit: 128000, Price: 1.25},
			{TokenLimit: 2000000, Price: 2.50},
		},
		OutputPriceTiers: []PriceTier{
			{TokenLimit: 128000, Price: 5.00},
			{TokenLimit: 2000000, Price: 10.00},
		},
	},
	"gemini-1.5-pro-002": {
		Provider:       LLMBackendGemini,
		ModelID:        "gemini-1.5-pro-002",
		MaxTokens:      8192,
		ContextWindow:  2000000,
		SupportsImages: true,
		InputPriceTiers: []PriceTier{
			{TokenLimit: 128000, Price: 1.25},
			{TokenLimit: 2000000, Price: 2.50},
		},
		OutputPriceTiers: []PriceTier{
			{TokenLimit: 128000, Price: 5.00},
			{TokenLimit: 2000000, Price: 10.00},
		},
	},

	// Google Gemini 1.5 Flash
	"gemini-1.5-flash": {
		Provider:       LLMBackendGemini,
		ModelID:        "gemini-1.5-flash-002",
		MaxTokens:      8192,
		ContextWindow:  1000000,
		SupportsImages: true,
		InputPriceTiers: []PriceTier{
			{TokenLimit: 128000, Price: 0.075},
			{TokenLimit: 1000000, Price: 0.15},
		},
		OutputPriceTiers: []PriceTier{
			{TokenLimit: 128000, Price: 0.30},
			{TokenLimit: 1000000, Price: 0.60},
		},
	},
	"gemini-1.5-flash-002": {
		Provider:       LLMBackendGemini,
		ModelID:        "gemini-1.5-flash-002",
		MaxTokens:      8192,
		ContextWindow:  1000000,
		SupportsImages: true,
		InputPriceTiers: []PriceTier{
			{TokenLimit: 128000, Price: 0.075},
			{TokenLimit: 1000000, Price: 0.15},
		},
		OutputPriceTiers: []PriceTier{
			{TokenLimit: 128000, Price: 0.30},
			{TokenLimit: 1000000, Price: 0.60},
		},
	},
}
