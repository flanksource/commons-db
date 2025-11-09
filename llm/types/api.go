package types

import (
	"context"

	"github.com/flanksource/commons-db/types"
)

// LLMBackend represents the supported LLM providers.
type LLMBackend string

const (
	LLMBackendOpenAI     LLMBackend = "openai"
	LLMBackendAnthropic  LLMBackend = "anthropic"
	LLMBackendGemini     LLMBackend = "gemini"
	LLMBackendClaudeCode LLMBackend = "claude-code"
)

// Provider is the interface that all LLM provider implementations must satisfy.
type Provider interface {
	// Execute sends a request to the LLM provider and returns the response.
	Execute(ctx context.Context, req ProviderRequest) (ProviderResponse, error)

	// GetModel returns the model name configured for this provider.
	GetModel() string

	// GetBackend returns the backend type for this provider.
	GetBackend() LLMBackend
}

// ProviderRequest contains all the information needed to make an LLM request.
type ProviderRequest struct {
	SystemPrompt     string
	Prompt           string
	MaxTokens        *int
	StructuredOutput interface{} // Schema for structured JSON output
	Model            string
	APIKey           string
	APIURL           string
}

// ProviderResponse contains the raw response from an LLM provider.
type ProviderResponse struct {
	Text             string
	StructuredData   interface{} // Populated if structured output was requested
	Model            string
	InputTokens      int
	OutputTokens     int
	ReasoningTokens  *int
	CacheReadTokens  *int
	CacheWriteTokens *int
}

// Connection represents a resolved connection with provider-specific configuration.
// It embeds types.HTTP to reuse URL and Bearer token fields with EnvVar lookup support.
type Connection struct {
	types.HTTP
	Backend LLMBackend
	Model   string
}
