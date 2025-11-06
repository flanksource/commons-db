package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/genai"
)

// executeGemini executes a request using the Google Gemini provider.
func executeGemini(ctx context.Context, req ProviderRequest) (ProviderResponse, error) {
	// Create Gemini client
	config := &genai.ClientConfig{
		APIKey: req.APIKey,
	}

	client, err := genai.NewClient(ctx, config)
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	// Set default model if not provided
	model := req.Model
	if model == "" {
		model = "gemini-2.5-pro-exp-03-25"
	}

	// Build contents
	var contents []*genai.Content

	// Add system prompt as user content (Gemini doesn't have system role)
	if req.SystemPrompt != "" {
		contents = append(contents, &genai.Content{
			Role: "user",
			Parts: []*genai.Part{
				{Text: req.SystemPrompt},
			},
		})
	}

	// Add user prompt
	contents = append(contents, &genai.Content{
		Role: "user",
		Parts: []*genai.Part{
			{Text: req.Prompt},
		},
	})

	// Build generation config
	var genConfig genai.GenerateContentConfig
	if req.MaxTokens != nil {
		genConfig.MaxOutputTokens = int32(*req.MaxTokens)
	}

	// Handle structured output via ResponseSchema
	if req.StructuredOutput != nil {
		schema, err := convertToGeminiSchema(req.StructuredOutput)
		if err != nil {
			return ProviderResponse{}, fmt.Errorf("failed to convert schema: %w", err)
		}
		genConfig.ResponseSchema = schema
		genConfig.ResponseMIMEType = "application/json"
	}

	// Execute request
	resp, err := client.Models.GenerateContent(ctx, model, contents, &genConfig)
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("Gemini request failed: %w", err)
	}

	if len(resp.Candidates) == 0 {
		return ProviderResponse{}, fmt.Errorf("no response from Gemini")
	}

	// Extract response text
	candidate := resp.Candidates[0]
	var text string
	if candidate.Content != nil && len(candidate.Content.Parts) > 0 {
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				text = part.Text
				break
			}
		}
	}

	// Handle structured output
	var structuredData interface{}
	if req.StructuredOutput != nil {
		if err := json.Unmarshal([]byte(text), req.StructuredOutput); err != nil {
			return ProviderResponse{}, fmt.Errorf("%w: %v", ErrSchemaValidation, err)
		}
		structuredData = req.StructuredOutput
		text = "" // Clear text when structured output is used
	}

	// Extract token usage
	inputTokens := 0
	outputTokens := 0
	if resp.UsageMetadata != nil {
		inputTokens = int(resp.UsageMetadata.PromptTokenCount)
		outputTokens = int(resp.UsageMetadata.CandidatesTokenCount)
	}

	return ProviderResponse{
		Text:           text,
		StructuredData: structuredData,
		Model:          model,
		InputTokens:    inputTokens,
		OutputTokens:   outputTokens,
	}, nil
}

// convertToGeminiSchema converts a Go struct to a Gemini Schema.
func convertToGeminiSchema(v interface{}) (*genai.Schema, error) {
	// FIXME: Implement proper Gemini schema generation from Go structs
	// For now, return a simple schema that accepts any object
	return &genai.Schema{
		Type: genai.TypeObject,
	}, nil
}
