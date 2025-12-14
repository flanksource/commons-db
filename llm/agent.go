package llm

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/ai"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/commons-db/llm/middleware"
	"github.com/flanksource/commons-db/llm/types"
	flanksourcecontext "github.com/flanksource/commons/context"
)

const AgentTypeLLM ai.AgentType = "llm"

// LLMAgent implements the Agent interface using the commons-db LLM client.
// It supports all LLM backends (OpenAI, Anthropic, Gemini, Claude Code CLI).
type LLMAgent struct {
	config  ai.AgentConfig
	client  types.Client
	session *ai.Session
	mu      sync.Mutex
}

func (la LLMAgent) String() string {
	return la.Pretty().ANSI()
}

func (la LLMAgent) Pretty() api.Text {
	t := la.config.Pretty()

	if la.session != nil {
		if la.session.Costs.Sum().TotalCost() > 0 {
			t = t.Append(" | ").Add(la.session.Costs.Sum().Pretty())
		}
		if la.session.ID != "" {
			t = t.Append(" | ").Append(la.session.ID, "max-w-[10ch]")
		}
	}
	return t

}

// NewLLMAgent creates a new LLM agent with the specified configuration.
//
// The agent can be configured to use any LLM backend by specifying the ai.Model name:
//   - OpenAI: gpt-4o, gpt-4-turbo, gpt-3.5-turbo
//   - Anthropic: claude-3-opus, claude-3.5-sonnet, claude-3.5-haiku
//   - Gemini: gemini-2.5-pro, gemini-2.0-flash, gemini-1.5-pro
//   - Claude Code CLI: claude-code-sonnet, claude-code-opus, claude-code-haiku
func NewLLMAgent(config ai.AgentConfig) (*LLMAgent, error) {
	if config.Model == "" {
		return nil, fmt.Errorf("--ai-model is required")
	}

	middlewares := []middleware.Option{
		middleware.WithDefaultLogging(),
	}

	if !config.NoCache {
		middlewares = append(middlewares, middleware.WithCache())
	}
	// Create LLM client with ai.Model inference
	client, err := NewClientWithModel(config.Model, middlewares...)
	if err != nil {
		return nil, fmt.Errorf("failed to create LLM client: %w", err)
	}

	// Initialize session for cost tracking
	session := ai.NewSession(config.SessionID, config.ProjectName)

	return &LLMAgent{
		config:  config,
		client:  client,
		session: session,
	}, nil
}

// GetType returns the agent type.
func (la *LLMAgent) GetType() ai.AgentType {
	return AgentTypeLLM
}

// GetConfig returns the agent configuration.
func (la *LLMAgent) GetConfig() ai.AgentConfig {
	return la.config
}

// Listai.Models returns available ai.Models for all LLM backends.
func (la *LLMAgent) ListModels(ctx context.Context) ([]ai.Model, error) {
	// Return ai.Models from the LLM cost registry
	models := []ai.Model{
		// OpenAI
		{ID: "gpt-4o", Name: "GPT-4o", Provider: "openai", InputPrice: 2.50, OutputPrice: 10.00, MaxTokens: 128000},
		{ID: "gpt-4o-mini", Name: "GPT-4o Mini", Provider: "openai", InputPrice: 0.150, OutputPrice: 0.600, MaxTokens: 128000},
		{ID: "gpt-4-turbo", Name: "GPT-4 Turbo", Provider: "openai", InputPrice: 10.00, OutputPrice: 30.00, MaxTokens: 128000},
		{ID: "gpt-3.5-turbo", Name: "GPT-3.5 Turbo", Provider: "openai", InputPrice: 0.50, OutputPrice: 1.50, MaxTokens: 16385},
		{ID: "o1", Name: "o1", Provider: "openai", InputPrice: 15.00, OutputPrice: 60.00, MaxTokens: 200000},
		{ID: "o1-mini", Name: "o1 Mini", Provider: "openai", InputPrice: 3.00, OutputPrice: 12.00, MaxTokens: 128000},

		// Anthropic
		{ID: "claude-3.7-sonnet", Name: "Claude 3.7 Sonnet", Provider: "anthropic", InputPrice: 3.00, OutputPrice: 15.00, MaxTokens: 200000},
		{ID: "claude-3.5-sonnet", Name: "Claude 3.5 Sonnet", Provider: "anthropic", InputPrice: 3.00, OutputPrice: 15.00, MaxTokens: 200000},
		{ID: "claude-3-opus", Name: "Claude 3 Opus", Provider: "anthropic", InputPrice: 15.00, OutputPrice: 75.00, MaxTokens: 200000},
		{ID: "claude-3.5-haiku", Name: "Claude 3.5 Haiku", Provider: "anthropic", InputPrice: 0.80, OutputPrice: 4.00, MaxTokens: 200000},

		// Gemini
		{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Provider: "gemini", InputPrice: 1.25, OutputPrice: 10.00, MaxTokens: 1000000},
		{ID: "gemini-2.0-flash", Name: "Gemini 2.0 Flash", Provider: "gemini", InputPrice: 0.00, OutputPrice: 0.00, MaxTokens: 1000000},
		{ID: "gemini-1.5-pro", Name: "Gemini 1.5 Pro", Provider: "gemini", InputPrice: 1.25, OutputPrice: 5.00, MaxTokens: 2000000},
		{ID: "gemini-1.5-flash", Name: "Gemini 1.5 Flash", Provider: "gemini", InputPrice: 0.075, OutputPrice: 0.30, MaxTokens: 1000000},

		// Claude Code CLI
		{ID: "claude-code-sonnet", Name: "Claude Code Sonnet", Provider: "claude-code", InputPrice: 3.00, OutputPrice: 15.00, MaxTokens: 200000},
		{ID: "claude-code-opus", Name: "Claude Code Opus", Provider: "claude-code", InputPrice: 15.00, OutputPrice: 75.00, MaxTokens: 200000},
		{ID: "claude-code-haiku", Name: "Claude Code Haiku", Provider: "claude-code", InputPrice: 0.80, OutputPrice: 4.00, MaxTokens: 200000},
	}

	return models, nil
}

// ExecutePrompt processes a single prompt using the LLM client.
func (la *LLMAgent) ExecutePrompt(ctx context.Context, request ai.PromptRequest) (*ai.PromptResponse, error) {
	startTime := time.Now()

	// Create task for progress tracking
	task := clicky.StartTask(request.Name,
		func(ctx flanksourcecontext.Context, t *clicky.Task) (interface{}, error) {
			// Build LLM request
			req := la.client.NewRequest().
				WithPrompt(request.Prompt)

			// Add system prompt from context if present
			if systemPrompt, ok := request.Context["system"]; ok {
				req = req.WithSystemPrompt(systemPrompt)
			}

			// Add structured output if requested
			if request.StructuredOutput != nil {
				req = req.WithStructuredOutput(request.StructuredOutput)
			}

			// Add max tokens if configured
			if la.config.MaxTokens > 0 {
				req = req.WithMaxTokens(la.config.MaxTokens)
			}

			// Set default timeout (5 minutes)
			timeout := 5 * time.Minute
			if deadline, ok := ctx.Deadline(); ok {
				timeout = time.Until(deadline)
			}
			req = req.WithTimeout(timeout)

			// Execute request
			resp, err := req.Execute(ctx.Context)
			if err != nil {
				t.Errorf("LLM request failed: %v", err)
				return nil, err
			}

			return resp, nil
		},
		clicky.WithTimeout(5*time.Minute),
		clicky.WithModel(la.config.Model),
		clicky.WithPrompt(request.Prompt))

	// Wait for task completion
	for task.Status() == clicky.StatusPending || task.Status() == clicky.StatusRunning {
		select {
		case <-ctx.Done():
			task.Cancel()
			return &ai.PromptResponse{
				Request: request,
				Error:   ctx.Err().Error(),
			}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
			// Continue waiting
		}
	}

	// Get result
	result, err := task.GetResult()
	if err != nil {
		return &ai.PromptResponse{
			Request: request,
			Error:   fmt.Sprintf("task failed: %v", err),
		}, err
	}

	resp, ok := result.(*types.Response)
	if !ok {
		return &ai.PromptResponse{
			Request: request,
			Error:   "invalid result type",
		}, fmt.Errorf("invalid result type")
	}

	// Convert LLM response to PromptResponse
	duration := time.Since(startTime)

	// Calculate input and output costs proportionally, avoiding division by zero
	var inputCost, outputCost float64
	totalTokens := resp.CostInfo.InputTokens + resp.CostInfo.OutputTokens
	if totalTokens > 0 {
		inputCost = resp.CostInfo.Cost * float64(resp.CostInfo.InputTokens) / float64(totalTokens)
		outputCost = resp.CostInfo.Cost * float64(resp.CostInfo.OutputTokens) / float64(totalTokens)
	}

	promptResp := &ai.PromptResponse{
		Request:        request,
		Result:         resp.Text,
		StructuredData: resp.StructuredData,
		Model:          resp.Model,
		Duration:       duration,
		Costs: ai.Costs{
			{
				Model:        resp.Model,
				InputTokens:  resp.CostInfo.InputTokens,
				OutputTokens: resp.CostInfo.OutputTokens,
				TotalTokens:  totalTokens,
				InputCost:    inputCost,
				OutputCost:   outputCost,
			},
		},
	}

	// Add costs to session
	la.mu.Lock()
	for _, cost := range promptResp.Costs {
		la.session.AddCost(cost)
	}
	la.mu.Unlock()

	return promptResp, nil
}

// ExecuteBatch processes multiple prompts concurrently.
func (la *LLMAgent) ExecuteBatch(ctx context.Context, requests []ai.PromptRequest) (map[string]*ai.PromptResponse, error) {
	results := make(map[string]*ai.PromptResponse)
	resultsChan := make(chan struct {
		name     string
		response *ai.PromptResponse
		err      error
	}, len(requests))

	// Determine concurrency limit
	maxConcurrent := la.config.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 4 // Default
	}

	// Create semaphore for concurrency control
	sem := make(chan struct{}, maxConcurrent)

	// Launch goroutines for each request
	var wg sync.WaitGroup
	for _, req := range requests {
		wg.Add(1)
		go func(request ai.PromptRequest) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			// Execute prompt
			response, err := la.ExecutePrompt(ctx, request)
			resultsChan <- struct {
				name     string
				response *ai.PromptResponse
				err      error
			}{
				name:     request.Name,
				response: response,
				err:      err,
			}
		}(req)
	}

	// Close channel when all goroutines complete
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	// Collect results
	for result := range resultsChan {
		if result.err == nil {
			results[result.name] = result.response
		} else {
			// Return error response
			results[result.name] = &ai.PromptResponse{
				Request: ai.PromptRequest{Name: result.name},
				Error:   result.err.Error(),
			}
		}
	}

	return results, nil
}

// GetCosts returns accumulated costs for this session.
func (la *LLMAgent) GetCosts() ai.Costs {
	la.mu.Lock()
	defer la.mu.Unlock()
	return la.session.Costs.AggregateByModel()
}

// Close cleans up agent resources.
func (la *LLMAgent) Close() error {
	// LLM client doesn't require explicit cleanup
	return nil
}
