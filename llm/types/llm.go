package types

import (
	"context"
	"fmt"
	"time"
)

// Client provides a fluent interface for making LLM requests.
type Client interface {
	// NewRequest creates a new request builder for constructing LLM requests.
	NewRequest() *RequestBuilder
}

// client is the default implementation of Client.
type client struct{}

// NewClient creates a new LLM client.
func NewClient() Client {
	return &client{}
}

// NewRequest creates a new request builder.
func (c *client) NewRequest() *RequestBuilder {
	return &RequestBuilder{
		Timeout: 60 * time.Second, // Default timeout
	}
}

// RequestBuilder provides a fluent interface for building LLM requests.
type RequestBuilder struct {
	Provider         Provider
	SystemPrompt     string
	Prompt           string
	MaxTokens        *int
	Timeout          time.Duration
	StructuredOutput interface{}
}

// WithConnection sets the named connection to use for this request.
// The connection is resolved from the duty/connection registry.
func (b *RequestBuilder) WithProvider(provider Provider) *RequestBuilder {
	b.Provider = provider
	return b
}

// WithSystemPrompt sets the system prompt that establishes context and behavior.
func (b *RequestBuilder) WithSystemPrompt(prompt string) *RequestBuilder {
	b.SystemPrompt = prompt
	return b
}

// WithPrompt sets the user prompt (required).
func (b *RequestBuilder) WithPrompt(prompt string) *RequestBuilder {
	b.Prompt = prompt
	return b
}

// WithMaxTokens sets the maximum number of output tokens.
func (b *RequestBuilder) WithMaxTokens(n int) *RequestBuilder {
	b.MaxTokens = &n
	return b
}

// WithTimeout sets the request timeout duration.
func (b *RequestBuilder) WithTimeout(d time.Duration) *RequestBuilder {
	b.Timeout = d
	return b
}

// WithStructuredOutput configures the request to return structured JSON output
// matching the schema of the provided pointer to a struct.
func (b *RequestBuilder) WithStructuredOutput(schema interface{}) *RequestBuilder {
	b.StructuredOutput = schema
	return b
}

// Execute sends the request to the configured LLM provider and returns the response.
func (b *RequestBuilder) Execute(ctx context.Context) (*Response, error) {
	// Validation
	if b.Prompt == "" {
		return nil, fmt.Errorf("prompt is required for LLM request")
	}

	if b.Provider == nil {
		return nil, fmt.Errorf("provider is required for LLM request - use WithProvider() to set it")
	}

	// Apply timeout to context
	ctx, cancel := context.WithTimeout(ctx, b.Timeout)
	defer cancel()

	// Build provider request
	req := ProviderRequest{
		SystemPrompt:     b.SystemPrompt,
		Prompt:           b.Prompt,
		MaxTokens:        b.MaxTokens,
		StructuredOutput: b.StructuredOutput,
	}

	// Execute request
	resp, err := b.Provider.Execute(ctx, req)
	if err != nil {
		return nil, err
	}

	// Calculate cost
	costInfo, err := calculateCost(resp.Model, resp.InputTokens, resp.OutputTokens, resp.ReasoningTokens, resp.CacheReadTokens, resp.CacheWriteTokens)
	if err != nil {
		costErrMsg := err.Error()
		costInfo.CostError = &costErrMsg
	}

	return &Response{
		Text:           resp.Text,
		StructuredData: resp.StructuredData,
		CostInfo:       costInfo,
		Model:          resp.Model,
		Provider:       string(b.Provider.GetBackend()),
	}, nil
}

// Response contains the LLM response data, including text, structured output, and cost information.
type Response struct {
	// Text is the text response from the LLM (empty if structured output was requested).
	Text string

	// StructuredData contains the parsed structured output if WithStructuredOutput was used.
	StructuredData interface{}

	// CostInfo provides token usage and cost information for this request.
	CostInfo CostInfo

	// Model is the specific model that generated the response.
	Model string

	// Provider is the LLM provider (openai, anthropic, gemini).
	Provider string
}

// CostInfo contains token usage and cost information for an LLM request.
type CostInfo struct {
	InputTokens      int     `json:"inputTokens"`
	OutputTokens     int     `json:"outputTokens"`
	ReasoningTokens  *int    `json:"reasoningTokens,omitempty"`
	CacheReadTokens  *int    `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens *int    `json:"cacheWriteTokens,omitempty"`
	Cost             float64 `json:"cost"`
	Model            string  `json:"model"`
	CostError        *string `json:"costCalculationError,omitempty"`
}
