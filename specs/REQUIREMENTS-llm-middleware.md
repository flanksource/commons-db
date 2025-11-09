# Feature: LLM Middleware with Logging and Caching

## Overview

Create a generic, reusable middleware library for LLM operations within the commons-db package. The middleware will provide transparent caching and comprehensive logging for LLM provider interactions, following patterns established in clicky/ai while being provider-agnostic and composable.

**Problem Being Solved**:
- Reduce LLM API costs through intelligent caching
- Improve observability of LLM usage with structured logging
- Provide consistent interface across multiple LLM providers (OpenAI, Anthropic Claude, Google Gemini)

**Target Users**:
- Developers using commons-db llm package
- Applications requiring LLM functionality with cost control
- Systems needing audit trails of LLM interactions

## Functional Requirements

### FR-1: Middleware Wrapper Pattern

**Description**: Implement a wrapper pattern that wraps existing llm.Provider implementations to add middleware capabilities without modifying the core provider code.

**User Story**: As a developer, I want to add caching and logging to any LLM provider without changing the provider implementation, so that I can compose middleware features transparently.

**Acceptance Criteria**:
- [ ] Middleware implements the same llm.Provider interface
- [ ] Middleware can wrap any existing provider (OpenAI, Anthropic, Gemini)
- [ ] Multiple middleware can be chained together (e.g., logging → caching → provider)
- [ ] Wrapper pattern is transparent to callers (same API contract)
- [ ] Original provider behavior is preserved when middleware is disabled

### FR-2: SQLite-Based Caching Middleware

**Description**: Implement caching middleware using the exact same approach as clicky/ai, with SQLite storage, SHA256 cache keys, and comprehensive metadata tracking.

**User Story**: As an application operator, I want to cache LLM responses to reduce API costs and improve response times, so that repeated queries don't incur additional charges.

**Acceptance Criteria**:
- [ ] Cache uses SQLite database with schema matching clicky/ai implementation
- [ ] Cache key generated from `SHA256(prompt + model + temperature + max_tokens)`
- [ ] TTL-based expiration with configurable duration
- [ ] Bypass cache option available per-request via context or option
- [ ] Cache stores: prompt, response, tokens, cost, duration, timestamps
- [ ] Cache hit/miss statistics tracked and logged
- [ ] Expired entries automatically cleaned up in background
- [ ] Cache database location configurable with sensible default (`~/.cache/commons-llm.db`)

### FR-3: Comprehensive Logging Middleware

**Description**: Implement logging middleware that captures all LLM interactions with structured logs including request/response data, performance metrics, errors, and cost tracking.

**User Story**: As a developer, I want detailed logs of all LLM interactions including prompts, responses, tokens, costs, and errors, so that I can debug issues and track usage.

**Acceptance Criteria**:
- [ ] Log all requests with: prompt (truncated if >500 chars), model, temperature, max_tokens
- [ ] Log all responses with: result (truncated if >500 chars), tokens used, cost, cache hit/miss
- [ ] Performance metrics logged: total duration, model processing time
- [ ] Error tracking with: error message, stack trace context, retry attempts (if any)
- [ ] Structured JSON logging format for machine parsing
- [ ] Log levels configurable (DEBUG, INFO, WARN, ERROR)
- [ ] Sensitive data redaction capability for prompts/responses
- [ ] Context propagation with correlation IDs from Go context

### FR-4: Provider Support

**Description**: Support for multiple LLM providers with unified interface and provider-specific adapters.

**User Story**: As a developer, I want to use OpenAI, Anthropic Claude, or Google Gemini interchangeably through the same middleware interface, so that I can switch providers without code changes.

**Acceptance Criteria**:
- [ ] OpenAI provider support (GPT-3.5, GPT-4, GPT-4o)
- [ ] Anthropic Claude provider support (Claude 3/3.5 models)
- [ ] Google Gemini provider support (Gemini models)
- [ ] Provider-specific token counting and cost calculation
- [ ] Provider-specific error handling and response parsing
- [ ] Shared middleware configuration across all providers

### FR-5: Functional Options Configuration

**Description**: Configuration using functional options pattern for composable, clear middleware setup.

**User Story**: As a developer, I want to configure middleware features using functional options, so that I can easily enable/disable features and understand the configuration.

**Acceptance Criteria**:
- [ ] `WithCache(config CacheConfig)` option to enable caching
- [ ] `WithLogging(config LogConfig)` option to enable logging
- [ ] `WithContext(ctx context.Context)` for context propagation
- [ ] `WithNoCache()` to bypass cache for specific requests
- [ ] Sensible defaults when no options provided
- [ ] Options can be combined in any order
- [ ] Type-safe option functions with clear documentation

### FR-6: Context Propagation

**Description**: Proper Go context handling for cancellation, timeouts, and correlation ID tracking.

**User Story**: As a developer, I want context to propagate through the middleware chain, so that I can cancel requests, enforce timeouts, and trace operations.

**Acceptance Criteria**:
- [ ] All middleware methods accept `context.Context` as first parameter
- [ ] Context cancellation properly handled at each layer
- [ ] Correlation IDs extracted from context and added to logs
- [ ] Context values preserved across middleware chain
- [ ] Timeout handling respects context deadlines

## User Interactions

### For Application Developers

**Creating a wrapped provider:**
```go
import (
    "github.com/flanksource/commons-db/llm"
    "github.com/flanksource/commons-db/llm/middleware"
)

// Create base provider
baseProvider, err := llm.NewOpenAIProvider(apiKey)

// Wrap with middleware
provider := middleware.Wrap(baseProvider,
    middleware.WithCache(middleware.CacheConfig{
        TTL: 24 * time.Hour,
        DBPath: "~/.cache/llm.db",
    }),
    middleware.WithLogging(middleware.LogConfig{
        Level: "INFO",
        Format: "json",
    }),
)

// Use wrapped provider exactly like base provider
response, err := provider.Generate(ctx, prompt, options...)
```

**Bypassing cache for specific requests:**
```go
ctx := middleware.WithNoCache(context.Background())
response, err := provider.Generate(ctx, prompt, options...)
```

### For Library Maintainers

**Adding new provider:**
1. Implement `llm.Provider` interface
2. Test with existing middleware
3. Add provider-specific token/cost calculation
4. No middleware code changes needed

## Technical Considerations

### Integration Points

**Existing llm package:**
- Wraps existing `llm.Provider` interface implementations
- No changes to core provider implementations required
- Middleware is optional and can be added/removed transparently

**Database:**
- SQLite for cache storage (same as clicky/ai)
- Schema: `ai_cache` table with columns for prompt_hash, response, tokens, cost, timestamps
- Automatic migrations for schema updates

**Logging:**
- Uses standard Go `log/slog` package for structured logging
- Logs written to stderr by default, configurable output
- JSON format for structured log parsing

### Data Flow

1. **Request Flow**:
   ```
   Application → Middleware Wrapper → [Logging] → [Caching] → Provider → LLM API
   ```

2. **Cache Hit**:
   ```
   Application → Middleware → Cache Check (HIT) → Return cached response → Log cache hit
   ```

3. **Cache Miss**:
   ```
   Application → Middleware → Cache Check (MISS) → Provider → LLM API → Store in cache → Log response
   ```

### Security

- No authentication/authorization in middleware (handled by providers)
- Cache database file permissions: 0600 (user read/write only)
- Optional prompt/response redaction in logs (regex-based)
- API keys never logged

### Performance

- Cache lookups should be <5ms (SQLite indexed by cache_key)
- Logging should be async to not block request path
- Background cleanup of expired cache entries (hourly)
- No request queuing or throttling (kept simple)

## Success Criteria

- [ ] All three LLM providers (OpenAI, Claude, Gemini) work with middleware
- [ ] Cache hit rate >60% for repeated queries in test scenarios
- [ ] Logging overhead <5% of total request time
- [ ] Zero breaking changes to existing llm.Provider interface
- [ ] Documentation covers all configuration options with examples
- [ ] Mock-based tests achieve >80% code coverage

## Testing Requirements

### Mock-Based Unit Tests

**Cache Middleware Tests**:
- Mock provider that returns predictable responses
- Test cache key generation (same inputs → same key)
- Test cache hit/miss scenarios
- Test TTL expiration
- Test bypass cache option
- Test concurrent access to cache

**Logging Middleware Tests**:
- Mock provider to verify logging calls
- Test all log levels (DEBUG, INFO, WARN, ERROR)
- Test structured log format (valid JSON)
- Test context correlation ID propagation
- Test error logging with stack traces
- Test metric logging (tokens, cost, duration)

**Wrapper Pattern Tests**:
- Test multiple middleware chaining
- Test option application order
- Test provider interface compliance
- Test error propagation through layers

**Provider-Specific Tests**:
- Mock OpenAI API responses
- Mock Anthropic API responses
- Mock Gemini API responses
- Test token counting for each provider
- Test cost calculation for each provider

## Implementation Checklist

### Phase 1: Setup & Planning

- [ ] Review and validate requirements with stakeholders
- [ ] Study clicky/ai cache implementation in detail
- [ ] Design middleware wrapper interface
- [ ] Design cache schema (copy from clicky/ai)
- [ ] Design logging structure and format
- [ ] Identify affected files in commons-db/llm package

### Phase 2: Core Implementation

#### 2.1: Cache Middleware
- [ ] Copy and adapt cache package from clicky/ai to commons-db
- [ ] Implement cache.go with Get/Set/Clear methods
- [ ] Implement schema.sql with ai_cache table
- [ ] Add cache key generation (SHA256)
- [ ] Add TTL-based expiration logic
- [ ] Add background cleanup goroutine
- [ ] Add cache statistics tracking

#### 2.2: Logging Middleware
- [ ] Create middleware/logging.go
- [ ] Implement structured logging with slog
- [ ] Add request/response logging
- [ ] Add performance metrics logging
- [ ] Add error tracking with context
- [ ] Add correlation ID support
- [ ] Add log level filtering

#### 2.3: Middleware Wrapper
- [ ] Create middleware/middleware.go
- [ ] Implement wrapper struct that embeds llm.Provider
- [ ] Implement Wrap() function with functional options
- [ ] Implement option types (WithCache, WithLogging, etc.)
- [ ] Implement middleware chaining logic
- [ ] Add context propagation

#### 2.4: Provider Integration
- [ ] Update OpenAI provider for middleware compatibility
- [ ] Update Anthropic provider for middleware compatibility
- [ ] Add Gemini provider implementation
- [ ] Ensure all providers implement llm.Provider interface
- [ ] Add provider-specific token/cost calculations

### Phase 3: Testing

- [ ] Write cache middleware mock tests (hit/miss, TTL, bypass)
- [ ] Write logging middleware mock tests (levels, format, correlation)
- [ ] Write wrapper pattern tests (chaining, options)
- [ ] Write provider integration tests with mocks
- [ ] Write context propagation tests
- [ ] Verify >80% code coverage

### Phase 4: Documentation & Cleanup

- [ ] Write package documentation (godoc)
- [ ] Create usage examples in README
- [ ] Document all configuration options
- [ ] Document cache schema and migration strategy
- [ ] Code review and refactoring for clarity
- [ ] Verify all acceptance criteria met
- [ ] Update CLAUDE.md if new patterns introduced
