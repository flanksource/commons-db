package llm

import (
	"fmt"

	. "github.com/flanksource/commons-db/llm/types"
)

// openAIProvider implements the Provider interface for OpenAI.
type openAIProvider struct {
	apiKey string
	model  string
	apiURL string
}

// Execute sends a request to OpenAI.
func (p *openAIProvider) Execute(sess *Session, req ProviderRequest) (ProviderResponse, error) {
	req.Model = p.model
	req.APIKey = p.apiKey
	req.APIURL = p.apiURL
	return executeOpenAI(sess, req)
}

// GetModel returns the model name.
func (p *openAIProvider) GetModel() string {
	return p.model
}

// GetBackend returns the backend type.
func (p *openAIProvider) GetBackend() LLMBackend {
	return LLMBackendOpenAI
}

// GetOpenRouterModelID returns the OpenRouter model identifier.
func (p *openAIProvider) GetOpenRouterModelID() string {
	return "openai/" + p.model
}

// anthropicProvider implements the Provider interface for Anthropic.
type anthropicProvider struct {
	apiKey string
	model  string
	apiURL string
}

// Execute sends a request to Anthropic.
func (p *anthropicProvider) Execute(sess *Session, req ProviderRequest) (ProviderResponse, error) {
	req.Model = p.model
	req.APIKey = p.apiKey
	req.APIURL = p.apiURL
	return executeAnthropic(sess, req)
}

// GetModel returns the model name.
func (p *anthropicProvider) GetModel() string {
	return p.model
}

// GetBackend returns the backend type.
func (p *anthropicProvider) GetBackend() LLMBackend {
	return LLMBackendAnthropic
}

// GetOpenRouterModelID returns the OpenRouter model identifier.
func (p *anthropicProvider) GetOpenRouterModelID() string {
	return "anthropic/" + p.model
}

// geminiProvider implements the Provider interface for Google Gemini.
type geminiProvider struct {
	apiKey string
	model  string
	apiURL string
}

// Execute sends a request to Google Gemini.
func (p *geminiProvider) Execute(sess *Session, req ProviderRequest) (ProviderResponse, error) {
	req.Model = p.model
	req.APIKey = p.apiKey
	req.APIURL = p.apiURL
	return executeGemini(sess, req)
}

// GetModel returns the model name.
func (p *geminiProvider) GetModel() string {
	return p.model
}

// GetBackend returns the backend type.
func (p *geminiProvider) GetBackend() LLMBackend {
	return LLMBackendGemini
}

// GetOpenRouterModelID returns the OpenRouter model identifier.
func (p *geminiProvider) GetOpenRouterModelID() string {
	return "google/" + p.model
}

// claudeCodeProvider implements the Provider interface for Claude Code CLI.
type claudeCodeProvider struct {
	model string
}

// Execute sends a request to Claude Code CLI.
func (p *claudeCodeProvider) Execute(sess *Session, req ProviderRequest) (ProviderResponse, error) {
	req.Model = p.model
	return executeClaudeCode(sess, req)
}

// GetModel returns the model name.
func (p *claudeCodeProvider) GetModel() string {
	return p.model
}

// GetBackend returns the backend type.
func (p *claudeCodeProvider) GetBackend() LLMBackend {
	return LLMBackendClaudeCode
}

// GetOpenRouterModelID returns the OpenRouter model identifier.
// Claude Code CLI is not available on OpenRouter.
func (p *claudeCodeProvider) GetOpenRouterModelID() string {
	return ""
}

// NewOpenAIProvider creates a new OpenAI provider with the specified configuration.
func NewOpenAIProvider(apiKey, model, apiURL string) Provider {
	return &openAIProvider{
		apiKey: apiKey,
		model:  model,
		apiURL: apiURL,
	}
}

// NewAnthropicProvider creates a new Anthropic provider with the specified configuration.
func NewAnthropicProvider(apiKey, model, apiURL string) Provider {
	return &anthropicProvider{
		apiKey: apiKey,
		model:  model,
		apiURL: apiURL,
	}
}

// NewGeminiProvider creates a new Gemini provider with the specified configuration.
func NewGeminiProvider(apiKey, model, apiURL string) Provider {
	return &geminiProvider{
		apiKey: apiKey,
		model:  model,
		apiURL: apiURL,
	}
}

// NewClaudeCodeProvider creates a new Claude Code provider with the specified model.
func NewClaudeCodeProvider(model string) Provider {
	return &claudeCodeProvider{
		model: model,
	}
}

// NewProvider creates a provider from a connection configuration.
func NewProvider(conn *Connection) (Provider, error) {
	apiKey := conn.Bearer.ValueStatic
	apiURL := conn.URL.ValueStatic
	model := conn.Model

	switch conn.Backend {
	case LLMBackendOpenAI:
		return NewOpenAIProvider(apiKey, model, apiURL), nil
	case LLMBackendAnthropic:
		return NewAnthropicProvider(apiKey, model, apiURL), nil
	case LLMBackendGemini:
		return NewGeminiProvider(apiKey, model, apiURL), nil
	case LLMBackendClaudeCode:
		return NewClaudeCodeProvider(model), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrInvalidProvider, conn.Backend)
	}
}
