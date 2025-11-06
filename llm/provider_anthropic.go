package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/anthropic"
)

// executeAnthropic executes a request using the Anthropic provider.
func executeAnthropic(ctx context.Context, req ProviderRequest) (ProviderResponse, error) {
	var opts []anthropic.Option

	// Set API key
	if req.APIKey != "" {
		opts = append(opts, anthropic.WithToken(req.APIKey))
	}

	// Set base URL if provided
	if req.APIURL != "" {
		opts = append(opts, anthropic.WithBaseURL(req.APIURL))
	}

	// Set model
	if req.Model != "" {
		opts = append(opts, anthropic.WithModel(req.Model))
	}

	// Create Anthropic client
	client, err := anthropic.New(opts...)
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("failed to create Anthropic client: %w", err)
	}

	// Build messages
	messages := []llms.MessageContent{}
	if req.SystemPrompt != "" {
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeSystem, req.SystemPrompt))
	}

	prompt := req.Prompt

	// For structured output, append instructions to use tool format
	if req.StructuredOutput != nil {
		prompt += "\n\nYou MUST respond with valid JSON that matches the required schema. Do not include any other text."
	}

	messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, prompt))

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
		return ProviderResponse{}, fmt.Errorf("Anthropic request failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return ProviderResponse{}, fmt.Errorf("no response from Anthropic")
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
	var cacheReadTokens *int
	var cacheWriteTokens *int
	model := req.Model

	if choice.GenerationInfo != nil {
		genInfo := choice.GenerationInfo
		if val, exists := genInfo["InputTokens"]; exists {
			if tokens, ok := val.(int); ok {
				inputTokens = tokens
			}
		}
		if val, exists := genInfo["OutputTokens"]; exists {
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
		Text:             text,
		StructuredData:   structuredData,
		Model:            model,
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
	}, nil
}
