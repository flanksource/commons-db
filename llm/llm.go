package llm

import (
	"context"
	"time"

	"github.com/flanksource/commons-db/types"
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
		timeout: 60 * time.Second, // Default timeout
	}
}

// RequestBuilder provides a fluent interface for building LLM requests.
type RequestBuilder struct {
	connection       string
	model            string
	systemPrompt     string
	prompt           string
	maxTokens        *int
	timeout          time.Duration
	structuredOutput interface{}
}

// WithConnection sets the named connection to use for this request.
// The connection is resolved from the duty/connection registry.
func (b *RequestBuilder) WithConnection(name string) *RequestBuilder {
	b.connection = name
	return b
}

// WithSystemPrompt sets the system prompt that establishes context and behavior.
func (b *RequestBuilder) WithSystemPrompt(prompt string) *RequestBuilder {
	b.systemPrompt = prompt
	return b
}

// WithPrompt sets the user prompt (required).
func (b *RequestBuilder) WithPrompt(prompt string) *RequestBuilder {
	b.prompt = prompt
	return b
}

// WithMaxTokens sets the maximum number of output tokens.
func (b *RequestBuilder) WithMaxTokens(n int) *RequestBuilder {
	b.maxTokens = &n
	return b
}

// WithTimeout sets the request timeout duration.
func (b *RequestBuilder) WithTimeout(d time.Duration) *RequestBuilder {
	b.timeout = d
	return b
}

// WithStructuredOutput configures the request to return structured JSON output
// matching the schema of the provided pointer to a struct.
func (b *RequestBuilder) WithStructuredOutput(schema interface{}) *RequestBuilder {
	b.structuredOutput = schema
	return b
}

// Execute sends the request to the configured LLM provider and returns the response.
func (b *RequestBuilder) Execute(ctx context.Context) (*Response, error) {
	// Validation
	if b.prompt == "" {
		return nil, ErrMissingPrompt
	}

	// Apply timeout to context
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	var conn *Connection
	var err error

	// Resolve connection or build from model
	if b.connection != "" {
		conn, err = resolveConnection(ctx, b.connection)
		if err != nil {
			return nil, err
		}
	} else if b.model != "" {
		conn, err = buildConnectionFromModel(b.model)
		if err != nil {
			return nil, err
		}
	} else {
		return nil, ErrMissingConnection
	}

	// Get provider
	provider, err := getProvider(conn)
	if err != nil {
		return nil, err
	}

	// Build provider request
	model := conn.Model
	if model == "" {
		model = b.model
	}

	// Extract string values from EnvVar fields
	apiURL := conn.URL.ValueStatic
	apiKey := conn.Bearer.ValueStatic

	if apiKey == "" {
		return nil, ErrMissingAPIKey
	}

	req := ProviderRequest{
		SystemPrompt:     b.systemPrompt,
		Prompt:           b.prompt,
		MaxTokens:        b.maxTokens,
		StructuredOutput: b.structuredOutput,
		Model:            model,
		APIKey:           apiKey,
		APIURL:           apiURL,
	}

	// Execute request
	resp, err := provider.Execute(ctx, req)
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
		Provider:       string(conn.Backend),
	}, nil
}

// buildConnectionFromModel builds a connection from a model name and environment variables.
func buildConnectionFromModel(model string) (*Connection, error) {
	backend, err := inferBackendFromModel(model)
	if err != nil {
		return nil, err
	}

	apiKey, err := getAPIKeyFromEnv(backend)
	if err != nil {
		return nil, err
	}

	return &Connection{
		Backend: backend,
		Model:   model,
		HTTP: types.HTTP{
			Bearer: types.EnvVar{
				ValueStatic: apiKey,
			},
		},
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
