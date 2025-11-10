package llm

import (
	"encoding/json"
	"fmt"

	. "github.com/flanksource/commons-db/llm/types"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

// executeOpenAI executes a request using the OpenAI provider.
func executeOpenAI(sess *Session, req ProviderRequest) (ProviderResponse, error) {
	// Build client options
	opts := []option.RequestOption{}

	// Set API key
	if req.APIKey != "" {
		opts = append(opts, option.WithAPIKey(req.APIKey))
	}

	// Set base URL if provided
	if req.APIURL != "" {
		opts = append(opts, option.WithBaseURL(req.APIURL))
	}

	// Create OpenAI client
	client := openai.NewClient(opts...)

	// Set default model if not provided
	model := req.Model
	if model == "" {
		model = "gpt-4o"
	}

	// Build messages
	var messages []openai.ChatCompletionMessageParamUnion

	if req.SystemPrompt != "" {
		messages = append(messages, openai.ChatCompletionMessageParamUnion{
			OfSystem: &openai.ChatCompletionSystemMessageParam{
				Content: openai.ChatCompletionSystemMessageParamContentUnion{
					OfString: openai.String(req.SystemPrompt),
				},
			},
		})
	}

	messages = append(messages, openai.ChatCompletionMessageParamUnion{
		OfUser: &openai.ChatCompletionUserMessageParam{
			Content: openai.ChatCompletionUserMessageParamContentUnion{
				OfString: openai.String(req.Prompt),
			},
		},
	})

	// Build chat completion params
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(model),
		Messages: messages,
	}

	// Handle structured output
	if req.StructuredOutput != nil {
		schema, err := generateJSONSchema(req.StructuredOutput)
		if err != nil {
			return ProviderResponse{}, fmt.Errorf("failed to generate schema: %w", err)
		}

		// Convert schema to interface{} for OpenAI
		schemaBytes, err := json.Marshal(schema)
		if err != nil {
			return ProviderResponse{}, fmt.Errorf("failed to marshal schema: %w", err)
		}

		var schemaInterface interface{}
		if err := json.Unmarshal(schemaBytes, &schemaInterface); err != nil {
			return ProviderResponse{}, fmt.Errorf("failed to unmarshal schema: %w", err)
		}

		// Set response format to JSON schema
		params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:        "response",
					Description: openai.String("Structured response"),
					Schema:      schemaInterface,
					Strict:      openai.Bool(true),
				},
			},
		}
	}

	// Set max tokens if provided
	if req.MaxTokens != nil {
		params.MaxTokens = openai.Int(int64(*req.MaxTokens))
	}

	// Set temperature to 0 for deterministic output
	params.Temperature = openai.Float(0.0)

	// Execute request
	resp, err := client.Chat.Completions.New(sess.Context, params)
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("OpenAI request failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return ProviderResponse{}, fmt.Errorf("no response from OpenAI")
	}

	// Extract response
	choice := resp.Choices[0]
	text := choice.Message.Content

	// Handle structured output
	var structuredData interface{}
	if req.StructuredOutput != nil {
		// Use cleanup to handle any formatting issues (though OpenAI strict mode should not need it)
		if err := UnmarshalWithCleanup(text, req.StructuredOutput); err != nil {
			return ProviderResponse{}, fmt.Errorf("%w: %v", ErrSchemaValidation, err)
		}
		structuredData = req.StructuredOutput
		text = ""
	}

	// Extract token usage
	inputTokens := int(resp.Usage.PromptTokens)
	outputTokens := int(resp.Usage.CompletionTokens)
	var reasoningTokens *int

	// OpenAI o1 models have reasoning tokens
	if resp.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
		tokens := int(resp.Usage.CompletionTokensDetails.ReasoningTokens)
		reasoningTokens = &tokens
	}

	providerResp := ProviderResponse{
		Text:            text,
		StructuredData:  structuredData,
		Model:           resp.Model,
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
		ReasoningTokens: reasoningTokens,
		Raw:             resp,
	}

	// Track costs in session
	cost, err := calcOpenAICosts(resp)
	if err == nil {
		sess.AddCost(cost)
	}

	return providerResp, nil
}

// calcOpenAICosts calculates costs from OpenAI API response
func calcOpenAICosts(resp *openai.ChatCompletion) (Cost, error) {
	// Extract token counts
	inputTokens := int(resp.Usage.PromptTokens)
	outputTokens := int(resp.Usage.CompletionTokens)
	var reasoningTokens *int

	// OpenAI o1 models have reasoning tokens
	if resp.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
		tokens := int(resp.Usage.CompletionTokensDetails.ReasoningTokens)
		reasoningTokens = &tokens
	}

	// Convert to OpenRouter format for pricing lookup
	openRouterModel := "openai/" + resp.Model
	modelInfo, exists := GetModelInfo(openRouterModel)
	if !exists {
		return Cost{}, fmt.Errorf("model %s not found in pricing registry", resp.Model)
	}

	// Calculate input cost
	inputCost := float64(inputTokens) * modelInfo.InputPrice / 1_000_000

	// Calculate output cost
	outputCost := float64(outputTokens) * modelInfo.OutputPrice / 1_000_000

	// Add reasoning token cost (uses output pricing)
	if reasoningTokens != nil && *reasoningTokens > 0 {
		outputCost += float64(*reasoningTokens) * modelInfo.OutputPrice / 1_000_000
	}

	// Calculate total tokens
	totalTokens := inputTokens + outputTokens
	if reasoningTokens != nil {
		totalTokens += *reasoningTokens
	}

	return Cost{
		Model:        resp.Model,
		ModelType:    ModelTypeLLM,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  totalTokens,
		InputCost:    inputCost,
		OutputCost:   outputCost,
	}, nil
}
