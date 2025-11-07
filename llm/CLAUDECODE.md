# Claude Code CLI Provider

The Claude Code CLI provider enables integration with Claude Code CLI as an LLM backend, allowing applications to leverage Claude's capabilities through the CLI instead of direct API calls.

## Overview

The Claude Code provider executes the `claude-code` CLI as a subprocess and communicates with it via JSON over stdin/stdout. It supports:

- **Text generation**: Standard chat completions
- **Structured JSON output**: Type-safe responses using Go struct schemas
- **Token usage tracking**: Automatic cost calculation based on token usage
- **Per-operation timeouts**: 60s for text, 120s for structured output
- **Comprehensive error handling**: CLI not found, timeouts, stderr parsing, exit codes

## Prerequisites

### Install Claude Code CLI

The `claude-code` command must be installed and available in your system PATH:

```bash
# Install Claude Code CLI
npm install -g @anthropic-ai/claude-code

# Verify installation
claude-code --version
```

### Authentication

The Claude Code CLI handles authentication independently. No API key configuration is needed in your application.

## Usage

### Basic Text Generation

```go
package main

import (
	"context"
	"fmt"
	"github.com/flanksource/commons-db/llm"
)

func main() {
	ctx := context.Background()

	// Create client with Claude Code model
	client, err := llm.NewClientWithModel("claude-code-sonnet")
	if err != nil {
		panic(err)
	}

	// Execute request
	resp, err := client.NewRequest().
		WithSystemPrompt("You are a helpful Go programming assistant.").
		WithPrompt("Explain what Go interfaces are in one sentence.").
		Execute(ctx)

	if err != nil {
		panic(err)
	}

	fmt.Printf("Response: %s\n", resp.Text)
	fmt.Printf("Cost: $%.6f\n", resp.CostInfo.Cost)
}
```

### Structured JSON Output

```go
// Define your response structure
type CodeExplanation struct {
	Summary     string   `json:"summary"`
	KeyConcepts []string `json:"keyConcepts"`
	Difficulty  string   `json:"difficulty"`
}

func main() {
	ctx := context.Background()
	client, _ := llm.NewClientWithModel("claude-code-sonnet")

	var result CodeExplanation
	resp, err := client.NewRequest().
		WithPrompt("Explain Go interfaces").
		WithStructuredOutput(&result).
		Execute(ctx)

	if err != nil {
		panic(err)
	}

	// Access structured data
	explanation := resp.StructuredData.(*CodeExplanation)
	fmt.Printf("Summary: %s\n", explanation.Summary)
}
```

### Custom Timeouts

```go
resp, err := client.NewRequest().
	WithPrompt("Complex task...").
	WithTimeout(180 * time.Second). // 3 minutes
	Execute(ctx)
```

### Using Named Connections

```go
// Register connection in database
ctx.DB().Create(&models.Connection{
	Name: "claude-code-cli",
	Type: "llm",
	Properties: map[string]string{
		"backend": "claude-code",
		"model":   "claude-code-sonnet",
	},
})

// Use in code
client := llm.NewClient()
resp, err := client.NewRequest().
	WithConnection("claude-code-cli").
	WithPrompt("Hello world").
	Execute(ctx)
```

## Model Names

Use models with the `claude-code-` prefix to trigger the Claude Code provider:

| Model Name | Maps To | Use Case |
|------------|---------|----------|
| `claude-code-sonnet` | claude-sonnet-4 | Default, balanced performance |
| `claude-code-sonnet-4` | claude-sonnet-4 | Latest Sonnet model |
| `claude-code-sonnet-3.5` | claude-3-5-sonnet | Slightly older Sonnet |
| `claude-code-opus` | claude-3-opus | Most capable, expensive |
| `claude-code-haiku` | claude-3-5-haiku | Fastest, cheapest |

## CLI Communication Protocol

### Request Format (stdin)

The provider sends JSON requests to the CLI via stdin:

```json
{
	"systemPrompt": "You are a helpful assistant",
	"prompt": "User's question here",
	"model": "claude-sonnet-4",
	"schema": {
		"type": "object",
		"properties": {
			"summary": {"type": "string"},
			"concepts": {"type": "array", "items": {"type": "string"}}
		}
	}
}
```

### Response Format (stdout)

The CLI should return JSON responses via stdout:

```json
{
	"text": "Response text for normal requests",
	"structured": {"summary": "...", "concepts": [...]},
	"usage": {
		"inputTokens": 100,
		"outputTokens": 50,
		"reasoningTokens": 25,
		"cacheReadTokens": 10,
		"cacheWriteTokens": 5
	}
}
```

### Error Handling

Errors are communicated via:

1. **Exit codes**:
   - `0`: Success
   - `1`: General execution error
   - `2`: Invalid arguments
   - `3`: Authentication failed
   - `124`: Timeout

2. **stderr**: Error messages written to stderr are captured and included in error messages

## Timeouts

The provider uses different default timeouts based on operation type:

- **Text generation**: 60 seconds
- **Structured output**: 120 seconds (longer due to schema processing)

Override with `WithTimeout()`:

```go
client.NewRequest().
	WithTimeout(3 * time.Minute).
	Execute(ctx)
```

Timeouts account for:
- Process startup time (~100-500ms)
- Network latency (if CLI makes API calls)
- Claude model processing time
- JSON parsing and validation

## Cost Tracking

Token usage and costs are automatically tracked using the same pricing as Anthropic's models:

| Model | Input (per 1M tokens) | Output (per 1M tokens) |
|-------|----------------------|------------------------|
| claude-code-sonnet | $3.00 | $15.00 |
| claude-code-opus | $15.00 | $75.00 |
| claude-code-haiku | $0.80 | $4.00 |

Cache pricing (if supported):
- Cache writes: 1.25x input price
- Cache reads: 0.10x input price

Access cost information:

```go
resp, _ := client.NewRequest().WithPrompt("...").Execute(ctx)

fmt.Printf("Tokens: %d input, %d output\n",
	resp.CostInfo.InputTokens,
	resp.CostInfo.OutputTokens)
fmt.Printf("Cost: $%.6f\n", resp.CostInfo.Cost)
```

## Error Handling

### Common Errors

**CLI Not Found**

```go
_, err := llm.NewClientWithModel("claude-code-sonnet")
// err: claude-code CLI not found in PATH: exec: "claude-code": executable file not found in $PATH
```

**Solution**: Install Claude Code CLI and ensure it's in your PATH

**Timeout**

```go
_, err := client.NewRequest().
	WithTimeout(1 * time.Second).
	Execute(ctx)
// err: request timeout exceeded: operation timed out after 1s
```

**Solution**: Increase timeout or optimize your prompt

**Authentication Failed**

```go
// err: authentication failed: claude-code CLI exited with code 3: API key invalid
```

**Solution**: Re-authenticate with Claude Code CLI

**Schema Validation Failed**

```go
_, err := client.NewRequest().
	WithStructuredOutput(&MyStruct{}).
	Execute(ctx)
// err: response failed schema validation: json: cannot unmarshal string into Go struct field
```

**Solution**: Ensure your schema matches the expected response format

## Testing

A test program is provided in `hack/test_claudecode.go`:

```bash
# Build and run test
go run ./hack/test_claudecode.go
```

The test covers:
1. Basic text generation
2. Structured JSON output
3. Timeout handling

## Implementation Details

### Architecture

```
Application
     ↓
LLM Client
     ↓
Claude Code Provider
     ↓
exec.CommandContext("claude-code")
     ↓
Subprocess (stdin/stdout/stderr)
     ↓
Claude Code CLI
     ↓
Anthropic API
```

### Subprocess Management

- Each request creates a new subprocess (stateless)
- Context cancellation propagates to subprocess
- Proper resource cleanup on completion or error
- Concurrent requests supported (each gets own subprocess)

### Token Usage Parsing

Token counts are extracted from the CLI response JSON:

```go
{
	"usage": {
		"inputTokens": 100,      // Required
		"outputTokens": 50,      // Required
		"reasoningTokens": 25,   // Optional (extended thinking)
		"cacheReadTokens": 10,   // Optional (Anthropic cache)
		"cacheWriteTokens": 5    // Optional (Anthropic cache)
	}
}
```

Missing token information is handled gracefully (set to 0).

## Limitations

1. **No Streaming**: The provider does not support streaming responses
2. **Subprocess Overhead**: ~100-500ms startup time per request
3. **CLI Dependency**: Requires Claude Code CLI to be installed and updated
4. **No Multi-turn**: Each request is independent (no conversation state)
5. **Authentication**: Relies on CLI's authentication mechanism

## Future Enhancements

Potential improvements (not yet implemented):

- [ ] Support for streaming responses
- [ ] Persistent CLI process (reduce startup overhead)
- [ ] Multi-turn conversation support
- [ ] Support for Claude Code-specific features (tools, extended thinking)
- [ ] MCP (Model Context Protocol) integration
- [ ] File access capabilities

## Troubleshooting

### Debug Logging

Enable debug logging to see subprocess communication:

```go
// TODO: Add debug logging support
```

### Verify CLI Works

Test the CLI directly:

```bash
claude-code --help
echo '{"prompt": "Hello"}' | claude-code
```

### Check PATH

Ensure `claude-code` is in your PATH:

```bash
which claude-code
echo $PATH
```

## Related Documentation

- [LLM Package README](../README.md)
- [Provider Interface](./providers.go)
- [Requirements Specification](../specs/REQUIREMENTS-claude-code-cli-provider.md)
- [Anthropic Claude Documentation](https://docs.anthropic.com/)
