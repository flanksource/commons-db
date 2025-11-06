# Feature: Generic AI Model SDK for Commons-DB

## Overview

### Problem Statement
The incident-commander project contains a robust LLM integration package (~1,326 lines) that provides multi-provider AI model support, cost tracking, and structured output capabilities. This functionality would be valuable across multiple Flanksource projects, but currently exists only in incident-commander. We need a generic, reusable AI model SDK in commons-db that provides core LLM capabilities without incident-commander-specific business logic.

### Solution
Create a new `commons-db/llm/` package that provides a clean, fluent API for interacting with multiple LLM providers (OpenAI, Anthropic, Google Gemini). The SDK will focus on two core operations: simple prompts and JSON schema structured output, with full cost tracking and integration with the duty/connection registry.

### Target Users
- **Flanksource developers** building features that need AI capabilities
- **Commons-db consumers** (incident-commander, mission-control, config-db, etc.)
- **Internal systems** requiring LLM integration with cost visibility

### Design Principles
- **Independence**: Separate package, does not replace incident-commander's LLM code
- **Fluent API**: Builder pattern for clean, readable request construction
- **Cost transparency**: Full token and cost tracking for all operations
- **Connection integration**: Seamless integration with duty's connection registry
- **Provider agnostic**: Unified interface across OpenAI, Anthropic, and Gemini

## Functional Requirements

### FR-1: Multi-Provider Support
**Description**: The SDK must support three LLM providers through a unified interface: OpenAI (GPT models), Anthropic (Claude models), and Google Gemini. Provider selection is determined at runtime via configuration, with provider-specific implementations hidden behind a common interface.

**User Story**: As a developer, I want to switch between OpenAI, Anthropic, and Gemini providers without changing my application code, so that I can optimize for cost, performance, or availability.

**Acceptance Criteria**:
- [ ] Support OpenAI models (gpt-4o, o1, gpt-4-turbo, gpt-3.5-turbo)
- [ ] Support Anthropic models (claude-3.7-sonnet, claude-3.5-sonnet, claude-3-opus, claude-3.5-haiku)
- [ ] Support Google Gemini models (gemini-2.5-pro, gemini-2.0-flash, gemini-1.5-pro, gemini-1.5-flash)
- [ ] Provider specified via configuration, not compile-time dependency
- [ ] All three providers support both simple prompts and structured output
- [ ] Provider-specific authentication handled internally (API keys, tokens)
- [ ] Return consistent response format regardless of provider

**Technical Notes**:
- Use langchaingo for OpenAI and Anthropic
- Use Google's official genai SDK for Gemini
- Implement adapter pattern (similar to GeminiModelWrapper in incident-commander)

---

### FR-2: Simple Prompt Execution
**Description**: The SDK must support single-turn prompt execution with system and user messages. This is the foundational operation for all LLM interactions, allowing developers to send a prompt and receive a text response.

**User Story**: As a developer, I want to send a prompt with a system instruction and user message to an LLM, so that I can get AI-generated responses for my application.

**Acceptance Criteria**:
- [ ] Accept system prompt (optional) to set context/behavior
- [ ] Accept user prompt (required) as the main input
- [ ] Return text response from LLM
- [ ] Return token usage (input tokens, output tokens)
- [ ] Return cost information (calculated from tokens and pricing)
- [ ] Support model-specific configuration (model name, max tokens, timeout)
- [ ] Handle empty responses gracefully
- [ ] Provider-agnostic error handling with wrapped context

**API Example**:
```go
response, err := client.NewRequest().
    WithConnection("my-openai-connection").
    WithSystemPrompt("You are a helpful assistant").
    WithPrompt("Explain Kubernetes pods").
    WithMaxTokens(500).
    WithTimeout(30 * time.Second).
    Execute(ctx)

// response.Text: "Kubernetes pods are..."
// response.InputTokens: 42
// response.OutputTokens: 150
// response.Cost: 0.00123
```

---

### FR-3: JSON Schema Structured Output
**Description**: The SDK must support structured output via JSON schema validation, forcing the LLM to respond in a specific JSON format. This enables reliable data extraction and tool calling patterns.

**User Story**: As a developer, I want to define a JSON schema and have the LLM return data matching that schema, so that I can reliably parse and use the response in my application logic.

**Acceptance Criteria**:
- [ ] Accept JSON schema definition (Go struct or schema object)
- [ ] Force LLM to respond with JSON matching the schema
- [ ] Validate response against schema (where provider supports it)
- [ ] Handle provider-specific structured output mechanisms:
  - OpenAI: JSON schema strict mode
  - Anthropic: Prompt-based forcing with tool definition
  - Gemini: ResponseSchema with JSON MIME type
- [ ] Return parsed structured data (unmarshaled into Go struct)
- [ ] Return token usage and cost information
- [ ] Handle schema validation failures with clear errors

**API Example**:
```go
type Diagnosis struct {
    Headline        string `json:"headline"`
    Summary         string `json:"summary"`
    RecommendedFix  string `json:"recommended_fix"`
}

var result Diagnosis
response, err := client.NewRequest().
    WithConnection("anthropic-connection").
    WithSystemPrompt("You are a diagnostics expert").
    WithPrompt("Analyze this error: connection timeout").
    WithStructuredOutput(&result).
    Execute(ctx)

// result.Headline: "Network Connection Timeout"
// result.Summary: "The service failed to establish..."
// result.RecommendedFix: "1. Check network connectivity..."
```

---

### FR-4: Full Cost Tracking Engine
**Description**: The SDK must include a comprehensive cost tracking system with a ModelInfo registry that stores per-model pricing information, supporting tiered pricing, cache pricing, and reasoning tokens. Cost calculation must be accurate and maintainable.

**User Story**: As a developer, I want to see the exact cost of each LLM call broken down by tokens, so that I can monitor spending and optimize usage.

**Acceptance Criteria**:
- [ ] ModelInfo registry with pricing for all supported models
- [ ] Support tiered pricing (e.g., Gemini preview models with token thresholds)
- [ ] Support cache pricing (prompt caching for Anthropic)
- [ ] Support reasoning tokens (o1 models)
- [ ] Calculate cost per request based on input/output tokens
- [ ] Return detailed cost breakdown in response
- [ ] Handle unknown models gracefully (log warning, return $0 cost)
- [ ] Allow cost registry updates without code changes (config-driven)
- [ ] Store model metadata: max tokens, context window, capabilities

**Response Format**:
```go
type CostInfo struct {
    InputTokens      int      `json:"inputTokens"`
    OutputTokens     int      `json:"outputTokens"`
    ReasoningTokens  *int     `json:"reasoningTokens,omitempty"`
    CacheReadTokens  *int     `json:"cacheReadTokens,omitempty"`
    CacheWriteTokens *int     `json:"cacheWriteTokens,omitempty"`
    Cost             float64  `json:"cost"`
    Model            string   `json:"model"`
    CostError        *string  `json:"costCalculationError,omitempty"`
}
```

**Technical Notes**:
- Port cost engine from `incident-commander/llm/cost.go`
- Maintain pricing accuracy to 4 decimal places
- Include model info for all 20+ supported models

---

### FR-5: Named Connection Management
**Description**: The SDK must integrate with the duty/connection registry, allowing users to reference named connections instead of hardcoding credentials. Connections are resolved at runtime from the registry, extracting API keys, URLs, models, and provider types.

**User Story**: As a developer, I want to reference LLM connections by name (e.g., "production-openai"), so that I can manage credentials centrally and avoid hardcoding secrets.

**Acceptance Criteria**:
- [ ] Accept connection name as configuration parameter
- [ ] Resolve connection from duty/connection registry
- [ ] Extract API key from connection password field
- [ ] Extract API URL from connection URL field (if provided)
- [ ] Extract model name from connection properties (if provided)
- [ ] Map connection type to LLM backend (e.g., "openai" → LLMBackendOpenAI)
- [ ] Return clear error if connection not found
- [ ] Return clear error if connection missing required fields (e.g., no password/API key)
- [ ] Support connection-level defaults (model, URL) overridden by request-level config

**Connection Resolution Example**:
```go
// Connection "prod-anthropic" in registry:
// - Type: "anthropic"
// - Password: "sk-ant-..."
// - Properties: {"model": "claude-3.7-sonnet"}

response, err := client.NewRequest().
    WithConnection("prod-anthropic").  // Resolves from registry
    WithPrompt("Hello").
    Execute(ctx)
// Uses claude-3.7-sonnet model from connection properties
```

**Technical Notes**:
- Reuse pattern from `incident-commander/api/v1/playbook_actions.go` Populate() method
- Connection resolution should be lazy (at Execute() time, not NewRequest() time)

---

### FR-6: Configurable Request Options
**Description**: The SDK must support per-request configuration options for max tokens and timeout. These options allow developers to control resource usage and request duration without changing code.

**User Story**: As a developer, I want to set max tokens and timeout per request, so that I can control costs and prevent long-running requests from blocking my application.

**Acceptance Criteria**:
- [ ] Support max tokens limit configuration (e.g., WithMaxTokens(500))
- [ ] Default max tokens: provider-specific (or unlimited if provider allows)
- [ ] Support timeout duration configuration (e.g., WithTimeout(30 * time.Second))
- [ ] Default timeout: 60 seconds
- [ ] Max tokens enforced by provider (SDK passes to provider API)
- [ ] Timeout enforced by context cancellation
- [ ] Return timeout errors with clear context ("request exceeded 30s timeout")
- [ ] Return token limit errors if response truncated

**Configuration Validation**:
- Max tokens must be > 0 (if specified)
- Max tokens must be ≤ model's max token limit
- Timeout must be > 0 (if specified)
- Warn if max tokens + prompt tokens exceed model's context window

---

### FR-7: Fluent Builder API Pattern
**Description**: The SDK must provide a fluent, chainable builder API for constructing requests. The builder pattern improves code readability, makes optional parameters explicit, and provides a natural flow for request construction.

**User Story**: As a developer, I want to chain method calls to build my LLM request, so that I can write readable, self-documenting code.

**Acceptance Criteria**:
- [ ] Provide `client.NewRequest()` to start request building
- [ ] Support method chaining for all configuration options
- [ ] Provide `Execute(ctx)` as terminal method that sends request
- [ ] Support optional parameters without function overloading
- [ ] Required parameters enforced at Execute() time (e.g., prompt required)
- [ ] Return clear validation errors for missing required fields
- [ ] Allow request reuse (execute same request multiple times)
- [ ] Thread-safe request building (concurrent NewRequest() calls safe)

**API Design**:
```go
type Client interface {
    NewRequest() *RequestBuilder
}

type RequestBuilder struct {
    // Internal state
}

func (b *RequestBuilder) WithConnection(name string) *RequestBuilder
func (b *RequestBuilder) WithSystemPrompt(prompt string) *RequestBuilder
func (b *RequestBuilder) WithPrompt(prompt string) *RequestBuilder
func (b *RequestBuilder) WithMaxTokens(n int) *RequestBuilder
func (b *RequestBuilder) WithTimeout(d time.Duration) *RequestBuilder
func (b *RequestBuilder) WithStructuredOutput(schema interface{}) *RequestBuilder
func (b *RequestBuilder) Execute(ctx context.Context) (*Response, error)
```

**Usage Example**:
```go
// Simple prompt
resp1, _ := client.NewRequest().
    WithConnection("openai").
    WithPrompt("Hello").
    Execute(ctx)

// Structured output with all options
var result DiagnosisResult
resp2, _ := client.NewRequest().
    WithConnection("anthropic").
    WithSystemPrompt("You are a diagnostics expert").
    WithPrompt("Analyze error: OOMKilled").
    WithMaxTokens(1000).
    WithTimeout(45 * time.Second).
    WithStructuredOutput(&result).
    Execute(ctx)
```

---

## User Interactions / API Design

### Package Structure
```
commons-db/llm/
├── llm.go              # Client, RequestBuilder, Response types
├── providers.go        # Provider interface, OpenAI/Anthropic/Gemini implementations
├── cost.go             # ModelInfo registry, cost calculation
├── connection.go       # Named connection resolution from duty
├── errors.go           # Custom error types
└── llm_test.go         # Unit tests with mocked providers
```

### Primary Types

**Client Creation**:
```go
func NewClient() Client
```

**Request Building**:
```go
type RequestBuilder struct {
    connection      string
    systemPrompt    string
    prompt          string
    maxTokens       *int
    timeout         time.Duration
    structuredOutput interface{}
}
```

**Response Structure**:
```go
type Response struct {
    Text         string
    StructuredData interface{} // populated if WithStructuredOutput used
    CostInfo     CostInfo
    Model        string
    Provider     string
}

type CostInfo struct {
    InputTokens      int
    OutputTokens     int
    ReasoningTokens  *int
    CacheReadTokens  *int
    CacheWriteTokens *int
    Cost             float64
    Model            string
    CostError        *string
}
```

### Example Usage Scenarios

**1. Simple Prompt with OpenAI**:
```go
client := llm.NewClient()

response, err := client.NewRequest().
    WithConnection("openai-prod").
    WithPrompt("Explain Kubernetes services in 2 sentences").
    WithMaxTokens(100).
    Execute(ctx)

if err != nil {
    return fmt.Errorf("LLM request failed: %w", err)
}

fmt.Printf("Response: %s\n", response.Text)
fmt.Printf("Cost: $%.4f (%d input + %d output tokens)\n",
    response.CostInfo.Cost,
    response.CostInfo.InputTokens,
    response.CostInfo.OutputTokens)
```

**2. Structured Output with Anthropic**:
```go
type IssueAnalysis struct {
    Severity    string `json:"severity"`
    Category    string `json:"category"`
    Summary     string `json:"summary"`
    Action      string `json:"recommended_action"`
}

client := llm.NewClient()
var analysis IssueAnalysis

response, err := client.NewRequest().
    WithConnection("anthropic-prod").
    WithSystemPrompt("You are a Kubernetes expert analyzing issues").
    WithPrompt("Pod crash loop: ImagePullBackOff for nginx:latst").
    WithStructuredOutput(&analysis).
    WithTimeout(30 * time.Second).
    Execute(ctx)

if err != nil {
    return fmt.Errorf("analysis failed: %w", err)
}

fmt.Printf("Severity: %s\n", analysis.Severity)
fmt.Printf("Category: %s\n", analysis.Category)
fmt.Printf("Action: %s\n", analysis.Action)
```

**3. Cost-Aware Batch Processing**:
```go
client := llm.NewClient()
var totalCost float64

prompts := []string{"Prompt 1", "Prompt 2", "Prompt 3"}

for _, prompt := range prompts {
    resp, err := client.NewRequest().
        WithConnection("gemini-prod").
        WithPrompt(prompt).
        WithMaxTokens(200).
        Execute(ctx)

    if err != nil {
        log.Printf("Request failed: %v", err)
        continue
    }

    totalCost += resp.CostInfo.Cost
}

fmt.Printf("Total batch cost: $%.4f\n", totalCost)
```

---

## Technical Considerations

### Integration Points

**1. Duty Connection Registry**:
- SDK depends on `commons-db/db` for connection resolution
- Connection types map to providers:
  - "openai" → LLMBackendOpenAI
  - "anthropic" → LLMBackendAnthropic
  - "gemini" → LLMBackendGemini
- Connection password field stores API key
- Connection properties store optional model name

**2. Context Integration**:
- All operations require `context.Context` for cancellation
- Timeout implemented via context deadline
- SDK respects context cancellation for long-running requests

**3. Provider Libraries**:
- langchaingo for OpenAI and Anthropic
- Google genai SDK for Gemini
- Adapter pattern to unify interfaces

### Data Flow

```
User Code
    ↓
NewRequest() → RequestBuilder
    ↓ (chain methods)
WithConnection(), WithPrompt(), etc.
    ↓
Execute(ctx)
    ↓
Resolve Connection (duty registry)
    ↓
Select Provider (OpenAI/Anthropic/Gemini)
    ↓
Build Provider Request (format conversion)
    ↓
Call Provider API (HTTP)
    ↓
Parse Response (text or structured)
    ↓
Calculate Cost (ModelInfo registry)
    ↓
Return Response{Text, CostInfo, ...}
```

### Error Handling

**Error Types**:
- `ErrConnectionNotFound`: Named connection doesn't exist
- `ErrMissingAPIKey`: Connection has no password/API key
- `ErrInvalidProvider`: Unknown provider type
- `ErrMissingPrompt`: Execute() called without prompt
- `ErrTimeout`: Request exceeded timeout duration
- `ErrProviderAPI`: Provider API returned error (wrapped)
- `ErrSchemaValidation`: Structured output doesn't match schema

**Error Wrapping**:
```go
if err != nil {
    return nil, fmt.Errorf("failed to execute LLM request: %w", err)
}
```

### Security Considerations

- API keys never logged or exposed in errors
- Connection resolution validates credentials exist before use
- Provider SDKs handle TLS and authentication
- No credential caching (retrieved fresh per request)

### Performance Considerations

- No connection pooling (stateless design)
- No response caching (caller's responsibility)
- Token counting extracted from provider metadata (no local tokenizer)
- Cost calculation is O(1) lookup from registry

---

## Success Criteria

### Functional Success Criteria
- [ ] Successfully execute simple prompts with OpenAI, Anthropic, and Gemini
- [ ] Successfully execute structured output requests with all three providers
- [ ] Cost calculation accuracy within $0.0001 of actual pricing
- [ ] Named connections resolve correctly from duty registry
- [ ] Max tokens and timeout configurations work as expected
- [ ] Fluent API allows readable, chainable request building

### Technical Success Criteria
- [ ] All unit tests pass with ≥80% code coverage
- [ ] No direct credential exposure in logs or errors
- [ ] Response format consistent across all providers
- [ ] Provider-specific errors wrapped with context
- [ ] Package has no dependency on incident-commander code

### Quality Success Criteria
- [ ] Code follows Go best practices (effective Go, idiomatic patterns)
- [ ] All public functions have GoDoc comments
- [ ] Error messages are clear and actionable
- [ ] API is intuitive (minimal documentation needed for basic usage)

---

## Testing Requirements

### Unit Tests with Mocked Providers

**Test Coverage Areas**:

1. **Request Builder Tests**:
   - Test method chaining returns correct builder state
   - Test validation errors (missing prompt, invalid timeout)
   - Test default values (timeout, max tokens)
   - Test concurrent NewRequest() calls are safe

2. **Provider Tests** (with mocks):
   - Mock OpenAI client, verify request formatting
   - Mock Anthropic client, verify request formatting
   - Mock Gemini client, verify request formatting
   - Verify provider selection based on connection type
   - Test structured output formatting per provider

3. **Cost Calculation Tests**:
   - Test cost calculation for each model in registry
   - Test tiered pricing (Gemini preview models)
   - Test cache pricing (Anthropic)
   - Test reasoning tokens (OpenAI o1)
   - Test unknown model handling ($0 cost, warning logged)

4. **Connection Resolution Tests**:
   - Test successful connection resolution
   - Test connection not found error
   - Test missing API key error
   - Test connection type to provider mapping
   - Test connection property extraction (model name)

5. **Error Handling Tests**:
   - Test timeout enforcement via context
   - Test max tokens validation
   - Test provider API error wrapping
   - Test structured output schema validation failures

**Test Structure Example**:
```go
func TestRequestBuilder_SimplePrompt(t *testing.T) {
    mockProvider := &MockProvider{
        response: "Test response",
        inputTokens: 10,
        outputTokens: 20,
    }

    client := NewClientWithProvider(mockProvider)

    resp, err := client.NewRequest().
        WithPrompt("Test prompt").
        Execute(context.Background())

    assert.NoError(t, err)
    assert.Equal(t, "Test response", resp.Text)
    assert.Equal(t, 10, resp.CostInfo.InputTokens)
    assert.Equal(t, 20, resp.CostInfo.OutputTokens)
}
```

**Mock Provider Interface**:
```go
type MockProvider struct {
    response      string
    inputTokens   int
    outputTokens  int
    err           error
}

func (m *MockProvider) Execute(ctx context.Context, req Request) (Response, error) {
    if m.err != nil {
        return Response{}, m.err
    }
    return Response{
        Text: m.response,
        CostInfo: CostInfo{
            InputTokens: m.inputTokens,
            OutputTokens: m.outputTokens,
        },
    }, nil
}
```

---

## Implementation Checklist

### Phase 1: Setup & Planning
- [ ] Create `commons-db/llm/` directory structure
- [ ] Define package interfaces (Client, RequestBuilder, Response)
- [ ] Define provider interface for abstraction
- [ ] Set up test infrastructure with mock providers
- [ ] Document API design decisions

### Phase 2: Core Implementation
- [ ] Implement Client and NewClient() constructor
- [ ] Implement RequestBuilder with fluent methods
- [ ] Implement Request validation logic
- [ ] Implement Execute() method skeleton
- [ ] Implement connection resolution from duty registry
- [ ] Implement provider selection logic (OpenAI/Anthropic/Gemini)

### Phase 3: Provider Implementations
- [ ] Implement OpenAI provider adapter (via langchaingo)
- [ ] Implement Anthropic provider adapter (via langchaingo)
- [ ] Implement Gemini provider adapter (via genai SDK)
- [ ] Implement simple prompt execution for all providers
- [ ] Implement JSON schema structured output for all providers
- [ ] Handle provider-specific authentication

### Phase 4: Cost Tracking Engine
- [ ] Port ModelInfo registry from incident-commander
- [ ] Implement cost calculation function with tiered pricing
- [ ] Add cache pricing support (Anthropic)
- [ ] Add reasoning token support (OpenAI o1)
- [ ] Populate registry with pricing for 20+ models
- [ ] Implement cost error handling for unknown models

### Phase 5: Configuration & Error Handling
- [ ] Implement max tokens configuration
- [ ] Implement timeout configuration via context
- [ ] Define custom error types (ErrConnectionNotFound, etc.)
- [ ] Implement error wrapping for provider errors
- [ ] Add validation errors for missing required fields
- [ ] Test timeout enforcement

### Phase 6: Testing
- [ ] Write unit tests for RequestBuilder (20+ test cases)
- [ ] Write unit tests for provider adapters (30+ test cases)
- [ ] Write unit tests for cost calculation (15+ test cases)
- [ ] Write unit tests for connection resolution (10+ test cases)
- [ ] Write unit tests for error handling (15+ test cases)
- [ ] Verify ≥80% code coverage

### Phase 7: Documentation & Cleanup
- [ ] Add GoDoc comments to all public types and functions
- [ ] Create usage examples in package documentation
- [ ] Review code for Go best practices
- [ ] Run gofmt and golangci-lint
- [ ] Verify all acceptance criteria met
- [ ] Create summary of implementation decisions

---

## Appendix: Design Decisions

### Why Fluent/Builder Pattern?
The fluent API pattern was chosen over other designs (match incident-commander, options pattern, config struct) because:
- **Readability**: Method chaining reads naturally, self-documenting code
- **Flexibility**: Optional parameters without function overloading
- **Type safety**: Compiler catches invalid method sequences
- **Extensibility**: Easy to add new options without breaking existing code
- **Common in Go**: Used by popular libraries (gRPC, AWS SDK v2)

### Why Independent Package?
The SDK is designed as an independent package rather than replacing incident-commander's LLM code because:
- **Risk mitigation**: Incident-commander's LLM code is production-tested
- **Gradual adoption**: Projects can adopt SDK at their own pace
- **Focused scope**: SDK focuses on core operations, not all features
- **Separation of concerns**: Business logic (playbooks, tools) stays in incident-commander

### Why OpenAI/Anthropic/Gemini Only?
Ollama support was excluded because:
- **Different use case**: Self-hosted, development/testing focused
- **No cost tracking**: Free/self-hosted models don't need pricing registry
- **Simplified scope**: Three cloud providers covers production use cases
- **Future addition**: Can be added later without breaking API

### Why No Streaming?
Streaming responses were excluded from initial scope because:
- **Complexity**: Requires different API design (channels or callbacks)
- **Limited use cases**: Most Flanksource features use complete responses
- **Future enhancement**: Can be added as separate interface without breaking existing API
- **Focused MVP**: Prioritize simple prompts and structured output first

### Why No Conversation History?
Multi-turn conversation support was excluded because:
- **Application responsibility**: Most apps manage conversation state themselves
- **Simple API**: Single-turn requests are easier to reason about and test
- **Stateless design**: No client-side state management required
- **Future enhancement**: Can be added as helper function or separate interface