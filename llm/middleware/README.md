# LLM Middleware

Composable middleware for LLM providers with caching and logging capabilities.

## Overview

This package provides middleware wrappers for LLM providers, enabling transparent
caching and comprehensive logging without modifying provider implementations.
Middleware can be composed using functional options to create flexible processing
pipelines.

## Features

- **Caching Middleware**: SQLite-based response caching with TTL support
- **Logging Middleware**: Structured logging with slog, including request/response
  details and performance metrics
- **Context Propagation**: Full Go context support for cancellation, timeouts,
  and correlation IDs
- **Composable Design**: Chain multiple middleware using functional options
- **Zero Provider Changes**: Works with any `llm.Provider` implementation

## Installation

```go
import (
    "github.com/flanksource/commons-db/llm"
    "github.com/flanksource/commons-db/llm/middleware"
    "github.com/flanksource/commons-db/llm/cache"
)
```

## Quick Start

### Using Middleware at the Provider Level

The middleware wraps `llm.Provider` implementations. Here's how to use it:

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/flanksource/commons-db/llm"
    "github.com/flanksource/commons-db/llm/middleware"
    "github.com/flanksource/commons-db/llm/cache"
)

// Example: Wrapping a provider with caching and logging
func example() error {
    // Step 1: Create cache instance
    c, err := cache.New(cache.Config{
        DBPath:  "~/.cache/llm.db",
        TTL:     24 * time.Hour,
    })
    if err != nil {
        return err
    }
    defer c.Close()

    // Step 2: Get a base provider (implementation-specific)
    // This example shows the pattern - actual provider creation
    // depends on your setup
    var baseProvider llm.Provider // from your provider factory

    // Step 3: Wrap with middleware
    provider := middleware.Wrap(baseProvider,
        middleware.WithCacheInstance(c),
        middleware.WithDefaultLogging(),
    )

    // Step 4: Use the wrapped provider
    ctx := context.Background()
    resp, err := provider.Execute(ctx, llm.ProviderRequest{
        Prompt: "What is the capital of France?",
        Model:  "gpt-4o",
    })
    if err != nil {
        return err
    }

    fmt.Println("Response:", resp.Text)
    fmt.Printf("Tokens: %d input, %d output\n",
        resp.InputTokens, resp.OutputTokens)
    return nil
}
```

### Caching Only

```go
// Create cache
c, _ := cache.New(cache.Config{
    DBPath: "~/.cache/llm.db",
    TTL:    24 * time.Hour,
})
defer c.Close()

// Wrap provider with caching only
provider := middleware.Wrap(baseProvider,
    middleware.WithCacheInstance(c),
)
```

### Logging Only

```go
// Wrap provider with default logging
provider := middleware.Wrap(baseProvider,
    middleware.WithDefaultLogging(),
)

// Or with custom logger and level
logger := middleware.NewJSONLogger(slog.LevelDebug)
provider := middleware.Wrap(baseProvider,
    middleware.WithLoggerAndLevel(logger, slog.LevelDebug),
)
```

### Custom Cache Configuration

```go
c, _ := cache.New(cache.Config{
    DBPath:  "/custom/path/cache.db",
    TTL:     48 * time.Hour,       // 48 hour cache
    NoCache: false,
    Debug:   true,                  // Enable debug output
})

provider := middleware.Wrap(baseProvider,
    middleware.WithCache(middleware.CacheConfig{
        Cache: c,
        TTL:   48 * time.Hour,
    }),
)
```

### Custom Logging Configuration

```go
logConfig := middleware.LogConfig{
    Logger:           slog.Default(),
    Level:            slog.LevelInfo,
    TruncatePrompt:   1000,  // Truncate prompts > 1000 chars
    TruncateResponse: 1000,  // Truncate responses > 1000 chars
    LogRequestBody:   true,
    LogResponseBody:  true,
}

provider := middleware.Wrap(baseProvider,
    middleware.WithLogging(logConfig),
)
```

## Context Features

### Bypass Cache

```go
// Bypass cache for specific request
ctx := middleware.WithNoCache(context.Background())
resp, err := provider.Execute(ctx, request)
```

### Correlation IDs

```go
// Add correlation ID for request tracing
ctx := middleware.WithCorrelationID(context.Background(), "req-12345")
resp, err := provider.Execute(ctx, request)

// Retrieve correlation ID
correlationID := middleware.GetCorrelationID(ctx)
```

### Request Timeouts

```go
// Context timeout works through middleware chain
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

resp, err := provider.Execute(ctx, request)
```

## Middleware Chaining

Middleware is applied in order, creating a processing chain:

```go
provider := middleware.Wrap(baseProvider,
    middleware.WithDefaultLogging(),     // 1. Logs request
    middleware.WithCacheInstance(cache), // 2. Checks cache
)
// Flow: Request → Logging → Caching → Provider → Response
```

The order matters for observability:
- Logging first captures all requests (cache hits and misses)
- Caching second avoids unnecessary provider calls

## Cache Management

### View Cache Statistics

```go
stats, err := c.GetStats()
for _, stat := range stats {
    fmt.Printf("Model: %s, Requests: %d, Cost: $%.4f\n",
        stat.Model, stat.TotalRequests, stat.TotalCost)
}
```

### Clear Cache

```go
// Clear all cache entries
err := c.Clear()
```

### Cache Entry Details

Each cached entry includes:
- Prompt and response
- Token counts (input, output, reasoning, cache read/write)
- Cost in USD
- Duration in milliseconds
- Model and provider
- Temperature and max_tokens
- Created/accessed/expires timestamps

## Log Output

### Info Level (Default)

```json
{
  "time": "2025-01-09T10:15:30Z",
  "level": "INFO",
  "msg": "LLM request completed",
  "model": "gpt-4o",
  "duration": "1.234s",
  "input_tokens": 10,
  "output_tokens": 20,
  "total_tokens": 30,
  "correlation_id": "req-12345"
}
```

### Debug Level

Includes full prompt and response (truncated):

```json
{
  "time": "2025-01-09T10:15:30Z",
  "level": "DEBUG",
  "msg": "LLM request started",
  "model": "gpt-4o",
  "prompt": "What is the capital of France...",
  "max_tokens": 1000,
  "correlation_id": "req-12345"
}
```

### Error Level

```json
{
  "time": "2025-01-09T10:15:30Z",
  "level": "ERROR",
  "msg": "LLM request failed",
  "model": "gpt-4o",
  "duration": "0.5s",
  "error": "API rate limit exceeded",
  "correlation_id": "req-12345"
}
```

## Testing

The package includes comprehensive mock-based tests:

```bash
go test ./llm/middleware -v
```

Test coverage includes:
- Cache hit/miss scenarios
- TTL expiration
- Cache bypass with context
- Different prompts and models
- Middleware chaining
- Context propagation

## Performance

- Cache lookups: <5ms (SQLite with indexes)
- Logging overhead: <5% of total request time
- Background cleanup: Hourly for expired entries
- Database: WAL mode for concurrent access

## Best Practices

1. **Always defer cache.Close()** to ensure cleanup
2. **Use context timeouts** to prevent hung requests
3. **Enable debug logging** during development only
4. **Set appropriate TTL** based on use case (24h default)
5. **Monitor cache stats** to optimize hit rates
6. **Use correlation IDs** for distributed tracing

## Architecture

```
Application
    ↓
middleware.Wrap(provider, options...)
    ↓
Logging Middleware (if enabled)
    ↓ (logs request)
Caching Middleware (if enabled)
    ↓ (checks cache)
Base Provider
    ↓ (calls LLM API)
Response
    ↑ (cached & logged)
Application
```

## Error Handling

Middleware is designed to be resilient:
- Cache errors don't fail requests (logged as warnings)
- Logging errors don't block execution
- Provider errors are propagated normally
- Context cancellation is respected at all layers

## Cache Schema

The SQLite cache includes two tables:

### llm_cache

Stores individual responses with:
- Full prompt and response text
- Token metrics (input, output, reasoning, cache)
- Cost and duration
- Model and provider metadata
- Timestamps and TTL

### llm_stats

Aggregates daily statistics:
- Request counts per model/provider
- Total tokens and costs
- Average duration
- Cache hit/miss rates

See `cache/schema.go` for the complete schema definition.
