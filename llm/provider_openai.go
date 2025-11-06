package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"
)

// executeOpenAI executes a request using the OpenAI provider.
func executeOpenAI(ctx context.Context, req ProviderRequest) (ProviderResponse, error) {
	var opts []openai.Option

	// Set API key
	if req.APIKey != "" {
		opts = append(opts, openai.WithToken(req.APIKey))
	}

	// Set base URL if provided
	if req.APIURL != "" {
		opts = append(opts, openai.WithBaseURL(req.APIURL))
	}

	// Set model
	if req.Model != "" {
		opts = append(opts, openai.WithModel(req.Model))
	}

	// FIXME: Handle structured output via JSON schema when langchaingo supports it
	// For now, structured output is handled via prompt engineering

	// Create OpenAI client
	client, err := openai.New(opts...)
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("failed to create OpenAI client: %w", err)
	}

	// Build messages
	messages := []llms.MessageContent{}
	if req.SystemPrompt != "" {
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeSystem, req.SystemPrompt))
	}
	messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, req.Prompt))

	// Build call options
	callOpts := []llms.CallOption{
		llms.WithTemperature(0), // Deterministic output
	}
	if req.MaxTokens != nil {
		callOpts = append(callOpts, llms.WithMaxTokens(*req.MaxTokens))
	}

	// Execute request
	resp, err := client.GenerateContent(ctx, messages, callOpts...)
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("OpenAI request failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return ProviderResponse{}, fmt.Errorf("no response from OpenAI")
	}

	// Extract response
	choice := resp.Choices[0]
	text := choice.Content

	// Handle structured output
	var structuredData interface{}
	if req.StructuredOutput != nil {
		if err := json.Unmarshal([]byte(text), req.StructuredOutput); err != nil {
			return ProviderResponse{}, fmt.Errorf("%w: %v", ErrSchemaValidation, err)
		}
		structuredData = req.StructuredOutput
		text = "" // Clear text when structured output is used
	}

	// Extract token usage from generation info
	inputTokens := 0
	outputTokens := 0
	var reasoningTokens *int
	model := req.Model

	if choice.GenerationInfo != nil {
		genInfo := choice.GenerationInfo
		if val, exists := genInfo["PromptTokens"]; exists {
			if tokens, ok := val.(int); ok {
				inputTokens = tokens
			}
		}
		if val, exists := genInfo["CompletionTokens"]; exists {
			if tokens, ok := val.(int); ok {
				outputTokens = tokens
			}
		}
		if val, exists := genInfo["Model"]; exists {
			if modelStr, ok := val.(string); ok && modelStr != "" {
				model = modelStr
			}
		}
	}

	return ProviderResponse{
		Text:            text,
		StructuredData:  structuredData,
		Model:           model,
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
		ReasoningTokens: reasoningTokens,
	}, nil
}
