package llm

import (
	"fmt"

	. "github.com/flanksource/commons-db/llm/types"
	"google.golang.org/genai"
)

// executeGemini executes a request using the Google Gemini provider.
func executeGemini(sess *Session, req ProviderRequest) (ProviderResponse, error) {
	// Create Gemini client
	config := &genai.ClientConfig{
		APIKey: req.APIKey,
	}

	client, err := genai.NewClient(sess.Context, config)
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
	resp, err := client.Models.GenerateContent(sess.Context, model, contents, &genConfig)
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
		// Use cleanup to handle any formatting issues
		if err := UnmarshalWithCleanup(text, req.StructuredOutput); err != nil {
			return ProviderResponse{}, fmt.Errorf("%w: %v", ErrSchemaValidation, err)
		}
		structuredData = req.StructuredOutput
		text = ""
	}

	// Extract token usage
	inputTokens := 0
	outputTokens := 0
	if resp.UsageMetadata != nil {
		inputTokens = int(resp.UsageMetadata.PromptTokenCount)
		outputTokens = int(resp.UsageMetadata.CandidatesTokenCount)
	}

	providerResp := ProviderResponse{
		Text:           text,
		StructuredData: structuredData,
		Model:          model,
		InputTokens:    inputTokens,
		OutputTokens:   outputTokens,
		Raw:            resp,
	}

	// Track costs in session
	cost, err := calcGeminiCosts(resp, model)
	if err == nil {
		sess.AddCost(cost)
	}

	return providerResp, nil
}

// calcGeminiCosts calculates costs from Gemini API response
func calcGeminiCosts(resp *genai.GenerateContentResponse, model string) (Cost, error) {
	// Extract token counts
	inputTokens := 0
	outputTokens := 0
	if resp.UsageMetadata != nil {
		inputTokens = int(resp.UsageMetadata.PromptTokenCount)
		outputTokens = int(resp.UsageMetadata.CandidatesTokenCount)
	}

	// Convert to OpenRouter format for pricing lookup
	openRouterModel := "google/" + model
	modelInfo, exists := GetModelInfo(openRouterModel)
	if !exists {
		return Cost{}, fmt.Errorf("model %s not found in pricing registry", model)
	}

	// Calculate input cost
	inputCost := float64(inputTokens) * modelInfo.InputPrice / 1_000_000

	// Calculate output cost
	outputCost := float64(outputTokens) * modelInfo.OutputPrice / 1_000_000

	return Cost{
		Model:        model,
		ModelType:    ModelTypeLLM,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  inputTokens + outputTokens,
		InputCost:    inputCost,
		OutputCost:   outputCost,
	}, nil
}

// convertToGeminiSchema converts a Go struct to a Gemini Schema.
func convertToGeminiSchema(v interface{}) (*genai.Schema, error) {
	// Generate JSON schema first
	jsonSchema, err := generateJSONSchema(v)
	if err != nil {
		return nil, fmt.Errorf("failed to generate JSON schema: %w", err)
	}

	// Convert JSON schema to Gemini schema
	return jsonSchemaToGemini(jsonSchema), nil
}

// jsonSchemaToGemini converts a JSONSchema to a Gemini Schema
func jsonSchemaToGemini(js *JSONSchema) *genai.Schema {
	schema := &genai.Schema{}

	// Map type
	switch js.Type {
	case "object":
		schema.Type = genai.TypeObject
		if js.Properties != nil {
			schema.Properties = make(map[string]*genai.Schema)
			for name, prop := range js.Properties {
				propCopy := prop
				schema.Properties[name] = jsonSchemaToGemini(&propCopy)
			}
		}
		if len(js.Required) > 0 {
			schema.Required = js.Required
		}
	case "array":
		schema.Type = genai.TypeArray
		if js.Items != nil {
			schema.Items = jsonSchemaToGemini(js.Items)
		}
	case "string":
		schema.Type = genai.TypeString
	case "integer":
		schema.Type = genai.TypeInteger
	case "number":
		schema.Type = genai.TypeNumber
	case "boolean":
		schema.Type = genai.TypeBoolean
	}

	// Add description if present
	if js.Description != "" {
		schema.Description = js.Description
	}

	return schema
}
