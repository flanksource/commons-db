package llm

import (
	"context"
	"fmt"
)

// LLMBackend represents the supported LLM providers.
type LLMBackend string

const (
	LLMBackendOpenAI    LLMBackend = "openai"
	LLMBackendAnthropic LLMBackend = "anthropic"
	LLMBackendGemini    LLMBackend = "gemini"
)

// Provider is the interface that all LLM provider implementations must satisfy.
type Provider interface {
	// Execute sends a request to the LLM provider and returns the response.
	Execute(ctx context.Context, req ProviderRequest) (ProviderResponse, error)
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
type Connection struct {
	Backend LLMBackend
	Model   string
	APIKey  string
	APIURL  string
}

// getProvider returns the appropriate provider based on the connection backend.
func getProvider(conn *Connection) (Provider, error) {
	switch conn.Backend {
	case LLMBackendOpenAI:
		return &openAIProvider{}, nil
	case LLMBackendAnthropic:
		return &anthropicProvider{}, nil
	case LLMBackendGemini:
		return &geminiProvider{}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrInvalidProvider, conn.Backend)
	}
}

// openAIProvider implements the Provider interface for OpenAI.
type openAIProvider struct{}

// Execute sends a request to OpenAI.
func (p *openAIProvider) Execute(ctx context.Context, req ProviderRequest) (ProviderResponse, error) {
	return executeOpenAI(ctx, req)
}

// anthropicProvider implements the Provider interface for Anthropic.
type anthropicProvider struct{}

// Execute sends a request to Anthropic.
func (p *anthropicProvider) Execute(ctx context.Context, req ProviderRequest) (ProviderResponse, error) {
	return executeAnthropic(ctx, req)
}

// geminiProvider implements the Provider interface for Google Gemini.
type geminiProvider struct{}

// Execute sends a request to Google Gemini.
func (p *geminiProvider) Execute(ctx context.Context, req ProviderRequest) (ProviderResponse, error) {
	return executeGemini(ctx, req)
}
