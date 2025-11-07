# Feature: Claude Code CLI LLM Provider Integration

## Overview

Integrate Claude Code CLI as a new LLM provider in the commons-db LLM system, enabling applications to leverage Claude Code's capabilities through the existing provider interface. This provider will execute Claude Code CLI as a subprocess in interactive mode, supporting both text generation and structured JSON output.

**Problem Being Solved**: Allow applications using the commons-db LLM system to access Claude Code CLI capabilities without requiring direct API integration, leveraging the CLI's built-in authentication and features.

**Target Users**:
- Developers using commons-db LLM client for AI operations
- Applications that want to use Claude Code CLI instead of direct Anthropic API
- Systems that prefer subprocess-based LLM integration

## Functional Requirements

### FR-1: Provider Interface Implementation
**Description**: Implement the standard `Provider` interface for Claude Code CLI, executing the CLI as a subprocess in interactive mode and communicating via stdin/stdout.

**User Story**: As a developer using the LLM client, I want to use Claude Code CLI as a provider so that I can leverage CLI-specific features while maintaining compatibility with existing code.

**Acceptance Criteria**:
- [ ] Implement `Provider` interface with `Execute(ctx context.Context, req ProviderRequest) (ProviderResponse, error)` method
- [ ] Execute `claude-code` command as subprocess in interactive mode
- [ ] Communicate with CLI via stdin/stdout using interactive protocol
- [ ] Support context cancellation for graceful shutdown
- [ ] Properly clean up subprocess resources on completion or error

### FR-2: Text Generation Support
**Description**: Support basic chat completion requests where the user provides a system prompt and user prompt, and receives a text response.

**User Story**: As an application developer, I want to send prompts to Claude Code CLI and receive text responses so that I can use it for standard LLM tasks.

**Acceptance Criteria**:
- [ ] Accept system prompt and user prompt from `ProviderRequest`
- [ ] Send formatted request to Claude Code CLI via stdin
- [ ] Parse text response from CLI stdout
- [ ] Return response in `ProviderResponse.Text` field
- [ ] Handle empty or missing prompts with appropriate errors

### FR-3: Structured JSON Output Support
**Description**: Support requests for structured JSON responses by providing a schema definition, enabling type-safe LLM interactions.

**User Story**: As a developer, I want to request structured JSON output from Claude Code CLI using Go struct schemas so that I can get type-safe responses.

**Acceptance Criteria**:
- [ ] Accept `StructuredOutput` schema from `ProviderRequest`
- [ ] Convert Go struct schema to format expected by Claude Code CLI
- [ ] Include schema in CLI request
- [ ] Parse JSON response from CLI stdout
- [ ] Validate response against schema
- [ ] Return parsed JSON in `ProviderResponse.StructuredData` field
- [ ] Return schema validation errors if response doesn't match

### FR-4: Token Usage and Cost Tracking
**Description**: Parse token usage information from Claude Code CLI output and calculate costs using existing pricing registry.

**User Story**: As a system administrator, I want to track token usage and costs for Claude Code CLI requests so that I can monitor LLM expenses.

**Acceptance Criteria**:
- [ ] Parse input token count from CLI output/logs
- [ ] Parse output token count from CLI output/logs
- [ ] Parse reasoning tokens if available (Claude extended thinking)
- [ ] Parse cache read/write tokens if available
- [ ] Populate `ProviderResponse` token count fields
- [ ] Use existing `CostInfo` calculation with appropriate Claude model pricing
- [ ] Handle missing token information gracefully (log warning, set counts to 0)

### FR-5: Model Name Pattern and Backend Selection
**Description**: Register Claude Code provider with model name prefix pattern to enable automatic backend selection.

**User Story**: As a developer, I want to use model names like `claude-code-sonnet` so that the system automatically routes to the Claude Code CLI provider.

**Acceptance Criteria**:
- [ ] Register provider with `LLMBackendClaudeCode` constant
- [ ] Implement model name inference for models starting with `claude-code-`
- [ ] Support model variants: `claude-code-sonnet`, `claude-code-opus`, `claude-code-haiku`, `claude-code-sonnet-4.5`
- [ ] Map model names to appropriate Claude Code CLI model arguments
- [ ] Return actual model name used in `ProviderResponse.Model` field

### FR-6: Comprehensive Error Handling
**Description**: Implement robust error handling for all failure scenarios including CLI not found, execution timeouts, stderr parsing, and exit code mapping.

**User Story**: As a developer, I want clear error messages when Claude Code CLI operations fail so that I can debug issues quickly.

**Acceptance Criteria**:
- [ ] Return `ErrCLINotFound` error if `claude-code` command is not in PATH
- [ ] Apply operation-specific timeout (default 60s) and return `ErrTimeout` on expiration
- [ ] Capture and parse error messages from CLI stderr
- [ ] Map non-zero exit codes to specific error types:
  - Exit code 1: General CLI error
  - Exit code 2: Invalid arguments/input
  - Exit code 3: Authentication failure (if applicable)
  - Exit code 124: Timeout
  - Other codes: Generic execution error
- [ ] Include stderr output in error messages for debugging
- [ ] Log subprocess start/end events for troubleshooting

### FR-7: CLI Executable Configuration
**Description**: Use `claude-code` command from system PATH with hardcoded default, assuming CLI is installed and accessible.

**User Story**: As a system administrator, I want the provider to automatically find the Claude Code CLI in PATH so that I don't need to configure paths manually.

**Acceptance Criteria**:
- [ ] Execute `claude-code` command without absolute path
- [ ] Rely on system PATH for executable resolution
- [ ] Return clear error if command not found in PATH
- [ ] Document requirement for `claude-code` to be installed and in PATH

### FR-8: Per-Operation Timeout Configuration
**Description**: Support different timeout values for different operation types (text generation vs structured output) via existing `WithTimeout()` method.

**User Story**: As a developer, I want to configure different timeouts for different operations so that complex structured output requests don't timeout prematurely.

**Acceptance Criteria**:
- [ ] Use default timeout from request (set via `WithTimeout()` method)
- [ ] If no timeout specified, use operation-specific defaults:
  - Text generation: 60 seconds
  - Structured output: 120 seconds (longer for schema processing)
- [ ] Apply timeout to subprocess execution
- [ ] Cancel context and kill subprocess when timeout exceeded
- [ ] Return `ErrTimeout` error with information about operation type

## User Interactions

### For Application Developers

**Using with connection registry**:
```go
// Register Claude Code CLI connection
ctx.DB().Create(&models.Connection{
    Name: "claude-code-cli",
    Type: "llm",
    Properties: map[string]string{
        "backend": "claude-code",
        "model": "claude-code-sonnet",
    },
})

// Use in code
client := llm.NewClient()
resp, err := client.NewRequest().
    WithConnection("claude-code-cli").
    WithPrompt("Explain Go interfaces").
    Execute(ctx)
```

**Using with direct model name**:
```go
// Model name triggers Claude Code provider automatically
client, err := llm.NewClientWithModel("claude-code-sonnet")
resp, err := client.NewRequest().
    WithSystemPrompt("You are a Go expert").
    WithPrompt("Explain interfaces").
    WithTimeout(90 * time.Second).
    Execute(ctx)
```

**Using with structured output**:
```go
type CodeExplanation struct {
    Summary     string   `json:"summary"`
    KeyConcepts []string `json:"keyConcepts"`
    Difficulty  string   `json:"difficulty"`
}

var result CodeExplanation
resp, err := client.NewRequest().
    WithConnection("claude-code-cli").
    WithPrompt("Explain Go interfaces").
    WithStructuredOutput(&result).
    WithTimeout(120 * time.Second).
    Execute(ctx)

// Access structured data
explanation := resp.StructuredData.(*CodeExplanation)
```

### User Flow

1. Application calls LLM client with `claude-code-*` model name or connection
2. Client resolves to Claude Code provider based on model prefix
3. Provider validates inputs (prompt present, schema valid if structured output)
4. Provider starts `claude-code` subprocess in interactive mode
5. Provider sends request (system prompt, user prompt, optional schema) via stdin
6. CLI processes request and returns response via stdout
7. Provider parses response (text or JSON), token counts, and any errors from stderr
8. Provider calculates cost using token counts and pricing registry
9. Provider returns unified `Response` with text/structured data + cost info
10. Application uses response and accesses cost information if needed

## Technical Considerations

### Integration Architecture
- **Subprocess execution**: Use `exec.CommandContext()` with context cancellation support
- **Interactive mode**: Start CLI in interactive/persistent mode, not one-shot invocations
- **Process management**: Properly handle stdin/stdout/stderr pipes
- **Resource cleanup**: Ensure subprocess termination on context cancel, timeout, or completion
- **Concurrent requests**: Each request gets its own subprocess (stateless per-request model)

### Communication Protocol
- **Input format**: JSON request sent to CLI stdin with structure:
  ```json
  {
    "systemPrompt": "string",
    "prompt": "string",
    "schema": {...},  // optional for structured output
    "model": "string"
  }
  ```
- **Output format**: JSON response from CLI stdout:
  ```json
  {
    "text": "string",           // for text generation
    "structured": {...},        // for structured output
    "usage": {
      "inputTokens": 100,
      "outputTokens": 50,
      "reasoningTokens": 25,    // optional
      "cacheReadTokens": 10,    // optional
      "cacheWriteTokens": 5     // optional
    }
  }
  ```
- **Error format**: Error messages in stderr, exit codes for failure types

### Backend Registration
- Add `LLMBackendClaudeCode LLMBackend = "claude-code"` constant to `llm/providers.go`
- Register in `getProvider()` dispatcher function
- Add model inference in `inferBackendFromModel()` for `claude-code-` prefix
- Update pricing registry in `cost.go` with Claude Code model prices (same as Anthropic)

### Dependencies
- Standard library `os/exec` for subprocess execution
- Standard library `encoding/json` for request/response parsing
- Existing `llm/schema.go` for JSON schema generation from Go structs
- Existing `llm/cost.go` for cost calculation

### Performance Considerations
- Subprocess startup overhead: ~100-500ms per request
- Interactive mode reduces overhead for multi-turn conversations
- Timeout values should account for:
  - Process startup time (~100-500ms)
  - Network latency if CLI makes API calls
  - Claude model processing time
  - JSON parsing and validation

### Security Considerations
- No API key handling (CLI manages authentication independently)
- Subprocess execution uses user's shell environment
- Input sanitization for prompt injection (same as existing providers)
- Validate JSON schema before sending to CLI
- Limit subprocess resource usage via context timeouts

## Success Criteria

- [ ] Claude Code CLI provider implements `Provider` interface
- [ ] Text generation requests work end-to-end
- [ ] Structured output requests work with JSON schema validation
- [ ] Token usage and costs are accurately tracked and calculated
- [ ] Model names with `claude-code-` prefix automatically route to provider
- [ ] All error scenarios are handled with clear error messages
- [ ] Per-operation timeouts work correctly
- [ ] CLI not found error is returned when `claude-code` not in PATH
- [ ] Integration is consistent with existing OpenAI, Anthropic, and Gemini providers
- [ ] Documentation includes setup instructions for installing Claude Code CLI

## Testing Requirements

**Note**: Initial implementation will skip tests, to be added later.

**Future test coverage** (when implemented):

### Unit Tests with Mocks
- Mock subprocess execution to test:
  - Request formatting (system prompt, user prompt, schema)
  - Response parsing (text, structured JSON, token counts)
  - Error handling (CLI not found, timeout, stderr, exit codes)
  - Timeout application (context cancellation)
  - Cost calculation (using mock token counts)

### Test Fixtures
- Sample CLI output for successful text generation
- Sample CLI output for successful structured output
- Sample CLI stderr for various error conditions
- Sample CLI exit codes and corresponding error messages

### Integration Tests (requires Claude Code CLI)
- End-to-end text generation request
- End-to-end structured output request
- Timeout scenario with slow-responding CLI
- CLI not found scenario
- Invalid schema scenario

## Implementation Checklist

### Phase 1: Core Provider Implementation
- [ ] Create `llm/provider_claudecode.go` file
- [ ] Add `LLMBackendClaudeCode` constant to `llm/providers.go`
- [ ] Implement `ClaudeCodeProvider` struct with `Execute()` method
- [ ] Implement subprocess execution with interactive mode
- [ ] Implement stdin request formatting (JSON)
- [ ] Implement stdout response parsing (JSON)
- [ ] Implement stderr error capture
- [ ] Implement exit code error mapping

### Phase 2: Request/Response Handling
- [ ] Add system prompt to request format
- [ ] Add user prompt to request format
- [ ] Add model name to request format
- [ ] Parse text response from CLI output
- [ ] Parse structured JSON response from CLI output
- [ ] Parse token usage from CLI output
- [ ] Calculate costs using existing pricing registry
- [ ] Populate `ProviderResponse` with all fields

### Phase 3: Structured Output Support
- [ ] Accept `StructuredOutput` schema from `ProviderRequest`
- [ ] Convert Go struct to JSON schema using existing `schema.go`
- [ ] Include schema in CLI request JSON
- [ ] Validate CLI response against schema
- [ ] Handle schema validation errors

### Phase 4: Error Handling and Timeouts
- [ ] Implement CLI not found error detection
- [ ] Implement per-operation timeout logic (60s text, 120s structured)
- [ ] Implement timeout error with context cancellation
- [ ] Implement stderr parsing and error message extraction
- [ ] Implement exit code mapping to error types
- [ ] Add error logging for debugging

### Phase 5: Backend Registration and Model Inference
- [ ] Register provider in `getProvider()` dispatcher
- [ ] Add model inference for `claude-code-` prefix in `inferBackendFromModel()`
- [ ] Support model variants: sonnet, opus, haiku, sonnet-4.5
- [ ] Map model names to CLI arguments
- [ ] Add Claude Code pricing to `cost.go` registry

### Phase 6: Integration and Manual Testing
- [ ] Test with simple text generation request
- [ ] Test with structured output request
- [ ] Test timeout scenarios
- [ ] Test CLI not found scenario
- [ ] Test error handling (invalid input, CLI errors)
- [ ] Verify token counting and cost calculation
- [ ] Test concurrent requests (multiple subprocesses)

### Phase 7: Documentation
- [ ] Add godoc comments to provider implementation
- [ ] Document `claude-code` CLI installation requirement
- [ ] Document model naming convention (`claude-code-*`)
- [ ] Add usage examples to package documentation
- [ ] Document timeout configuration recommendations
- [ ] Update main LLM package README with Claude Code provider

### Phase 8: Testing (Future)
- [ ] Write unit tests with mocked subprocess
- [ ] Create test fixtures for CLI output
- [ ] Add integration tests (optional, requires CLI)
- [ ] Verify test coverage for error paths
