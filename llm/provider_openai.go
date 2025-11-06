package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

// executeOpenAI executes a request using the OpenAI provider.
func executeOpenAI(ctx context.Context, req ProviderRequest) (ProviderResponse, error) {
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
	resp, err := client.Chat.Completions.New(ctx, params)
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
		if err := json.Unmarshal([]byte(text), req.StructuredOutput); err != nil {
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

	return ProviderResponse{
		Text:            text,
		StructuredData:  structuredData,
		Model:           resp.Model,
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
		ReasoningTokens: reasoningTokens,
	}, nil
}
