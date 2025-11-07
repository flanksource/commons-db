package llm

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// directClient is a client implementation that uses environment variables for API keys.
type directClient struct {
	model string
}

// NewClientWithModel creates a new LLM client for the specified model.
// The client automatically infers the provider from the model name and
// looks up the API key from environment variables:
//   - OpenAI models: OPENAI_API_KEY
//   - Anthropic models: ANTHROPIC_API_KEY
//   - Gemini models: GEMINI_API_KEY or GOOGLE_API_KEY
//
// Example usage:
//
//	client := llm.NewClientWithModel("gpt-4o")
//	resp, err := client.NewRequest().
//	    WithPrompt("Hello world").
//	    Execute(ctx)
func NewClientWithModel(model string) (Client, error) {
	if model == "" {
		return nil, fmt.Errorf("model cannot be empty")
	}

	// Verify provider can be inferred
	backend, err := inferBackendFromModel(model)
	if err != nil {
		return nil, err
	}

	// Verify API key is available
	if _, err := getAPIKeyFromEnv(backend); err != nil {
		return nil, err
	}

	return &directClient{model: model}, nil
}

// NewRequest creates a new request builder.
func (c *directClient) NewRequest() *RequestBuilder {
	return &RequestBuilder{
		model:   c.model,
		timeout: 60 * time.Second,
	}
}

// inferBackendFromModel infers the LLM provider from the model name.
func inferBackendFromModel(model string) (LLMBackend, error) {
	modelLower := strings.ToLower(model)

	// OpenAI models
	if strings.HasPrefix(modelLower, "gpt-") ||
		strings.HasPrefix(modelLower, "o1-") ||
		strings.HasPrefix(modelLower, "o3-") ||
		strings.Contains(modelLower, "davinci") ||
		strings.Contains(modelLower, "curie") ||
		strings.Contains(modelLower, "babbage") ||
		strings.Contains(modelLower, "ada") {
		return LLMBackendOpenAI, nil
	}

	// Anthropic models
	if strings.HasPrefix(modelLower, "claude-") {
		return LLMBackendAnthropic, nil
	}

	// Gemini models
	if strings.HasPrefix(modelLower, "gemini-") ||
		strings.HasPrefix(modelLower, "models/gemini-") {
		return LLMBackendGemini, nil
	}

	return "", fmt.Errorf("unable to infer provider from model name: %s", model)
}

// getAPIKeyFromEnv retrieves the API key for the specified backend from environment variables.
func getAPIKeyFromEnv(backend LLMBackend) (string, error) {
	var envVars []string

	switch backend {
	case LLMBackendOpenAI:
		envVars = []string{"OPENAI_API_KEY"}
	case LLMBackendAnthropic:
		envVars = []string{"ANTHROPIC_API_KEY"}
	case LLMBackendGemini:
		envVars = []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}
	default:
		return "", fmt.Errorf("unsupported backend: %s", backend)
	}

	for _, envVar := range envVars {
		if key := os.Getenv(envVar); key != "" {
			return key, nil
		}
	}

	return "", fmt.Errorf("API key not found in environment variables: %v", envVars)
}
