package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	. "github.com/flanksource/commons-db/llm/types"
)

// claudeCodeRequest represents the JSON request sent to Claude Code CLI stdin.
type claudeCodeRequest struct {
	SystemPrompt string      `json:"systemPrompt,omitempty"`
	Prompt       string      `json:"prompt"`
	Schema       interface{} `json:"schema,omitempty"`
	Model        string      `json:"model"`
}

// claudeCodeResponse represents the JSON response from Claude Code CLI stdout.
type claudeCodeResponse struct {
	Text       string                `json:"text,omitempty"`
	Structured interface{}           `json:"structured,omitempty"`
	Usage      *claudeCodeTokenUsage `json:"usage,omitempty"`
	Error      string                `json:"error,omitempty"`
}

// claudeCodeTokenUsage represents token usage information from Claude Code CLI.
type claudeCodeTokenUsage struct {
	InputTokens      int `json:"inputTokens"`
	OutputTokens     int `json:"outputTokens"`
	ReasoningTokens  int `json:"reasoningTokens,omitempty"`
	CacheReadTokens  int `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int `json:"cacheWriteTokens,omitempty"`
}

// executeClaudeCode executes a request using the Claude Code CLI provider.
func executeClaudeCode(sess *Session, req ProviderRequest) (ProviderResponse, error) {
	// Determine timeout based on operation type
	timeout := 60 * time.Second // Default for text generation
	if req.StructuredOutput != nil {
		timeout = 120 * time.Second // Longer timeout for structured output
	}

	// Check if context has a deadline and use the shorter timeout
	if deadline, ok := sess.Deadline(); ok {
		ctxTimeout := time.Until(deadline)
		if ctxTimeout < timeout {
			timeout = ctxTimeout
		}
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(sess.Context, timeout)
	defer cancel()

	// Set default model if not provided
	model := req.Model
	if model == "" {
		model = "claude-code-sonnet"
	}

	// Map claude-code-* model names to underlying Claude model names
	actualModel := mapClaudeCodeModel(model)

	// Build request JSON
	cliReq := claudeCodeRequest{
		SystemPrompt: req.SystemPrompt,
		Prompt:       req.Prompt,
		Model:        actualModel,
	}

	// Handle structured output
	if req.StructuredOutput != nil {
		schema, err := generateJSONSchema(req.StructuredOutput)
		if err != nil {
			return ProviderResponse{}, fmt.Errorf("failed to generate schema: %w", err)
		}
		cliReq.Schema = schema
	}

	// Marshal request to JSON
	reqBytes, err := json.Marshal(cliReq)
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Execute CLI subprocess
	cmd := exec.CommandContext(ctx, "claude-code")

	// Set up pipes
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Start the process
	if err := cmd.Start(); err != nil {
		if isCommandNotFound(err) {
			return ProviderResponse{}, fmt.Errorf("%w: %v", ErrCLINotFound, err)
		}
		return ProviderResponse{}, fmt.Errorf("failed to start claude-code CLI: %w", err)
	}

	// Write request to stdin
	if _, err := stdin.Write(reqBytes); err != nil {
		return ProviderResponse{}, fmt.Errorf("failed to write to stdin: %w", err)
	}
	if _, err := stdin.Write([]byte("\n")); err != nil {
		return ProviderResponse{}, fmt.Errorf("failed to write newline to stdin: %w", err)
	}
	stdin.Close()

	// Read stdout and stderr concurrently
	stdoutChan := make(chan []byte, 1)
	stderrChan := make(chan string, 1)
	errChan := make(chan error, 2)

	go func() {
		data, err := io.ReadAll(stdout)
		if err != nil {
			errChan <- fmt.Errorf("failed to read stdout: %w", err)
			return
		}
		stdoutChan <- data
	}()

	go func() {
		data, err := io.ReadAll(stderr)
		if err != nil {
			errChan <- fmt.Errorf("failed to read stderr: %w", err)
			return
		}
		stderrChan <- string(data)
	}()

	// Wait for process to complete
	waitChan := make(chan error, 1)
	go func() {
		waitChan <- cmd.Wait()
	}()

	var stdoutData []byte
	var stderrData string
	var waitErr error

	// Collect all results
	for i := 0; i < 3; i++ {
		select {
		case <-ctx.Done():
			cmd.Process.Kill()
			return ProviderResponse{}, fmt.Errorf("%w: operation timed out after %v", ErrTimeout, timeout)
		case err := <-errChan:
			return ProviderResponse{}, err
		case data := <-stdoutChan:
			stdoutData = data
		case data := <-stderrChan:
			stderrData = data
		case err := <-waitChan:
			waitErr = err
		}
	}

	// Check exit code
	if waitErr != nil {
		exitCode := getExitCode(waitErr)
		errorMsg := fmt.Sprintf("claude-code CLI exited with code %d", exitCode)
		if stderrData != "" {
			errorMsg += fmt.Sprintf(": %s", stderrData)
		}

		switch exitCode {
		case 1:
			return ProviderResponse{}, fmt.Errorf("%w: %s", ErrCLIExecutionFailed, errorMsg)
		case 2:
			return ProviderResponse{}, fmt.Errorf("invalid arguments: %s", errorMsg)
		case 3:
			return ProviderResponse{}, fmt.Errorf("authentication failed: %s", errorMsg)
		case 124:
			return ProviderResponse{}, fmt.Errorf("%w: %s", ErrTimeout, errorMsg)
		default:
			return ProviderResponse{}, fmt.Errorf("%w: %s", ErrCLIExecutionFailed, errorMsg)
		}
	}

	// Parse response JSON
	var cliResp claudeCodeResponse

	// Try to parse the response, looking for JSON in stdout
	stdoutStr := string(stdoutData)
	jsonStart := strings.Index(stdoutStr, "{")
	if jsonStart >= 0 {
		stdoutStr = stdoutStr[jsonStart:]
	}

	if err := json.Unmarshal([]byte(stdoutStr), &cliResp); err != nil {
		return ProviderResponse{}, fmt.Errorf("failed to parse CLI response: %w (output: %s)", err, stdoutStr)
	}

	// Check for error in response
	if cliResp.Error != "" {
		return ProviderResponse{}, fmt.Errorf("CLI returned error: %s", cliResp.Error)
	}

	// Handle structured output
	var structuredData interface{}
	if req.StructuredOutput != nil {
		if cliResp.Structured == nil {
			return ProviderResponse{}, fmt.Errorf("%w: no structured data in response", ErrSchemaValidation)
		}

		// Marshal and unmarshal to populate the schema struct
		structBytes, err := json.Marshal(cliResp.Structured)
		if err != nil {
			return ProviderResponse{}, fmt.Errorf("failed to marshal structured response: %w", err)
		}

		// Use cleanup to handle any formatting issues
		if err := UnmarshalWithCleanup(string(structBytes), req.StructuredOutput); err != nil {
			return ProviderResponse{}, fmt.Errorf("%w: %v", ErrSchemaValidation, err)
		}

		structuredData = req.StructuredOutput
	}

	// Extract token usage
	var inputTokens, outputTokens int
	var reasoningTokens, cacheReadTokens, cacheWriteTokens *int

	if cliResp.Usage != nil {
		inputTokens = cliResp.Usage.InputTokens
		outputTokens = cliResp.Usage.OutputTokens

		if cliResp.Usage.ReasoningTokens > 0 {
			tokens := cliResp.Usage.ReasoningTokens
			reasoningTokens = &tokens
		}

		if cliResp.Usage.CacheReadTokens > 0 {
			tokens := cliResp.Usage.CacheReadTokens
			cacheReadTokens = &tokens
		}

		if cliResp.Usage.CacheWriteTokens > 0 {
			tokens := cliResp.Usage.CacheWriteTokens
			cacheWriteTokens = &tokens
		}
	}

	// Determine response text
	text := cliResp.Text
	if req.StructuredOutput != nil {
		text = ""
	}

	providerResp := ProviderResponse{
		Text:             text,
		StructuredData:   structuredData,
		Model:            model, // Return original model name
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		ReasoningTokens:  reasoningTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
		Raw:              cliResp,
	}

	// Track costs in session
	cost, err := calcClaudeCodeCosts(cliResp.Usage, actualModel)
	if err == nil {
		sess.AddCost(cost)
	}

	return providerResp, nil
}

// calcClaudeCodeCosts calculates costs from Claude Code CLI response
func calcClaudeCodeCosts(usage *claudeCodeTokenUsage, model string) (Cost, error) {
	if usage == nil {
		return Cost{}, fmt.Errorf("no usage information in response")
	}

	// Extract token counts
	inputTokens := usage.InputTokens
	outputTokens := usage.OutputTokens
	var reasoningTokens *int
	var cacheReadTokens *int
	var cacheWriteTokens *int

	if usage.ReasoningTokens > 0 {
		tokens := usage.ReasoningTokens
		reasoningTokens = &tokens
	}

	if usage.CacheReadTokens > 0 {
		tokens := usage.CacheReadTokens
		cacheReadTokens = &tokens
	}

	if usage.CacheWriteTokens > 0 {
		tokens := usage.CacheWriteTokens
		cacheWriteTokens = &tokens
	}

	// Convert to OpenRouter format for pricing lookup
	openRouterModel := "anthropic/" + model
	modelInfo, exists := GetModelInfo(openRouterModel)
	if !exists {
		return Cost{}, fmt.Errorf("model %s not found in pricing registry", model)
	}

	// Calculate input cost
	inputCost := float64(inputTokens) * modelInfo.InputPrice / 1_000_000

	// Calculate output cost
	outputCost := float64(outputTokens) * modelInfo.OutputPrice / 1_000_000

	// Add reasoning token cost (uses output pricing)
	if reasoningTokens != nil && *reasoningTokens > 0 {
		outputCost += float64(*reasoningTokens) * modelInfo.OutputPrice / 1_000_000
	}

	// Add cache read cost (to input)
	if cacheReadTokens != nil && *cacheReadTokens > 0 {
		inputCost += float64(*cacheReadTokens) * modelInfo.CacheReadsPrice / 1_000_000
	}

	// Add cache write cost (to input)
	if cacheWriteTokens != nil && *cacheWriteTokens > 0 {
		inputCost += float64(*cacheWriteTokens) * modelInfo.CacheWritesPrice / 1_000_000
	}

	// Calculate total tokens
	totalTokens := inputTokens + outputTokens
	if reasoningTokens != nil {
		totalTokens += *reasoningTokens
	}
	if cacheReadTokens != nil {
		totalTokens += *cacheReadTokens
	}
	if cacheWriteTokens != nil {
		totalTokens += *cacheWriteTokens
	}

	return Cost{
		Model:        model,
		ModelType:    ModelTypeLLM,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  totalTokens,
		InputCost:    inputCost,
		OutputCost:   outputCost,
	}, nil
}

// mapClaudeCodeModel maps claude-code-* model names to actual Claude model names.
func mapClaudeCodeModel(model string) string {
	model = strings.TrimPrefix(model, "claude-code-")

	// Map common variants
	switch model {
	case "sonnet":
		return "claude-sonnet-4"
	case "sonnet-4", "sonnet-4.0":
		return "claude-sonnet-4"
	case "sonnet-3.5", "sonnet-3-5":
		return "claude-3-5-sonnet-20241022"
	case "opus":
		return "claude-3-opus-20240229"
	case "haiku":
		return "claude-3-5-haiku-20241022"
	default:
		// If already a full model name, return as-is
		if strings.HasPrefix(model, "claude-") {
			return model
		}
		// Default to sonnet 4
		return "claude-sonnet-4"
	}
}

// isCommandNotFound checks if an error is due to command not being found.
func isCommandNotFound(err error) bool {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return execErr.Err == exec.ErrNotFound
	}
	return false
}

// getExitCode extracts the exit code from an exec.ExitError.
func getExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// parseStderr attempts to extract meaningful error messages from stderr output.
func parseStderr(stderr string) string {
	if stderr == "" {
		return ""
	}

	// Use bufio.Scanner to parse stderr line by line
	scanner := bufio.NewScanner(strings.NewReader(stderr))
	var errLines []string

	for scanner.Scan() {
		line := scanner.Text()
		// Filter out common non-error log lines
		if strings.Contains(line, "error") || strings.Contains(line, "Error") ||
			strings.Contains(line, "failed") || strings.Contains(line, "Failed") {
			errLines = append(errLines, line)
		}
	}

	if len(errLines) > 0 {
		return strings.Join(errLines, "; ")
	}

	// If no error-like lines found, return first few lines
	lines := strings.Split(stderr, "\n")
	if len(lines) > 5 {
		lines = lines[:5]
	}
	return strings.Join(lines, "; ")
}
