package middleware

import (
	"log/slog"
	"os"
	"time"

	. "github.com/flanksource/commons-db/llm/types"
	"github.com/flanksource/commons/logger"
)

// LogConfig holds configuration for logging middleware
type LogConfig struct {
	Logger           logger.Logger // Custom logger (optional, defaults to slog.Default())
	Level            slog.Level    // Minimum log level (default: Info)
	TruncatePrompt   int           // Truncate prompts longer than this (0 = no truncation)
	TruncateResponse int           // Truncate responses longer than this (0 = no truncation)
	RedactSensitive  bool          // Enable sensitive data redaction
	LogRequestBody   bool          // Log full request details (default: true)
	LogResponseBody  bool          // Log full response details (default: true)
}

// DefaultLogConfig returns the default logging configuration
func DefaultLogConfig() LogConfig {
	return LogConfig{
		Logger:           logger.StandardLogger(),
		Level:            slog.LevelInfo,
		TruncatePrompt:   500,
		TruncateResponse: 500,
		RedactSensitive:  false,
		LogRequestBody:   true,
		LogResponseBody:  true,
	}
}

// loggingProvider wraps a Provider with logging capabilities
type loggingProvider struct {
	provider Provider
	config   LogConfig
}

// newLoggingProvider creates a new logging middleware
func newLoggingProvider(provider Provider, config LogConfig) Provider {
	if config.Logger == nil {
		config.Logger = logger.StandardLogger()
	}
	return &loggingProvider{
		provider: provider,
		config:   config,
	}
}

// GetModel returns the model name from the wrapped provider
func (l *loggingProvider) GetModel() string {
	return l.provider.GetModel()
}

// GetBackend returns the backend type from the wrapped provider
func (l *loggingProvider) GetBackend() LLMBackend {
	return l.provider.GetBackend()
}

// GetOpenRouterModelID returns the OpenRouter model identifier from the wrapped provider
func (l *loggingProvider) GetOpenRouterModelID() string {
	return l.provider.GetOpenRouterModelID()
}

// Execute implements the Provider interface with logging
func (l *loggingProvider) Execute(sess *Session, req ProviderRequest) (ProviderResponse, error) {
	startTime := time.Now()

	// Extract correlation ID from context if available
	correlationID, _ := sess.Value(correlationIDKey).(string)

	// Log request
	if l.config.LogRequestBody {
		if l.config.Logger.IsTraceEnabled() {
			// Use Pretty() for trace level - rich ANSI formatted output
			l.config.Logger.Tracef("LLM request started:\n%s", req.Pretty().ANSI())
		} else if l.config.Logger.Enabled(sess.Context, slog.LevelDebug) {
			// Use structured logging for debug level - compact output
			attrs := []slog.Attr{
				slog.String("model", req.Model),
				slog.String("prompt", l.truncate(req.Prompt, l.config.TruncatePrompt)),
			}
			if correlationID != "" {
				attrs = append(attrs, slog.String("correlation_id", correlationID))
			}
			if req.SystemPrompt != "" {
				attrs = append(attrs, slog.String("system_prompt", l.truncate(req.SystemPrompt, l.config.TruncatePrompt)))
			}
			if req.MaxTokens != nil {
				attrs = append(attrs, slog.Int("max_tokens", *req.MaxTokens))
			}

			l.config.Logger.LogAttrs(sess.Context, slog.LevelDebug, "LLM request started", attrs...)
		}
	}

	// Execute the actual request
	resp, err := l.provider.Execute(sess, req)
	duration := time.Since(startTime)

	// Log response or error
	if err != nil {
		attrs := []slog.Attr{
			slog.String("model", req.Model),
			slog.Duration("duration", duration),
			slog.String("error", err.Error()),
		}
		if correlationID != "" {
			attrs = append(attrs, slog.String("correlation_id", correlationID))
		}

		l.config.Logger.LogAttrs(sess.Context, slog.LevelError, "LLM request failed", attrs...)
		return resp, err
	}

	// Log successful response
	attrs := []slog.Attr{
		slog.String("model", resp.Model),
		slog.Duration("duration", duration),
		slog.Int("input_tokens", resp.InputTokens),
		slog.Int("output_tokens", resp.OutputTokens),
		slog.Int("total_tokens", resp.InputTokens+resp.OutputTokens),
	}

	if correlationID != "" {
		attrs = append(attrs, slog.String("correlation_id", correlationID))
	}

	if resp.ReasoningTokens != nil && *resp.ReasoningTokens > 0 {
		attrs = append(attrs, slog.Int("reasoning_tokens", *resp.ReasoningTokens))
	}

	if resp.CacheReadTokens != nil && *resp.CacheReadTokens > 0 {
		attrs = append(attrs, slog.Int("cache_read_tokens", *resp.CacheReadTokens))
	}

	if resp.CacheWriteTokens != nil && *resp.CacheWriteTokens > 0 {
		attrs = append(attrs, slog.Int("cache_write_tokens", *resp.CacheWriteTokens))
	}

	if l.config.LogResponseBody {
		if l.config.Logger.IsTraceEnabled() {
			// Use Pretty() for trace level - rich ANSI formatted output
			l.config.Logger.Tracef("LLM response:\n%s", resp.Pretty().ANSI())
		} else if l.config.Logger.Enabled(sess.Context, slog.LevelDebug) {
			// Add truncated response for debug level
			attrs = append(attrs, slog.String("response", l.truncate(resp.Text, l.config.TruncateResponse)))
		}
	}

	l.config.Logger.LogAttrs(sess.Context, slog.LevelInfo, "LLM request completed", attrs...)

	return resp, nil
}

// truncate truncates a string to the specified length
func (l *loggingProvider) truncate(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	if maxLen < 10 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// WithLogging returns a middleware option that adds logging capabilities
func WithLogging(config LogConfig) Option {
	return func(p Provider) (Provider, error) {
		return newLoggingProvider(p, config), nil
	}
}

// WithDefaultLogging returns a middleware option with default logging configuration
func WithDefaultLogging() Option {
	return WithLogging(DefaultLogConfig())
}

// WithLoggerAndLevel returns a middleware option with custom logger and level
func WithLoggerAndLevel(logger *slog.Logger, level slog.Level) Option {
	config := DefaultLogConfig()
	config.Logger = logger
	config.Level = level
	return WithLogging(config)
}

// NewJSONLogger creates a new JSON-formatted logger for structured logging
func NewJSONLogger(level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: level,
	}
	handler := slog.NewJSONHandler(os.Stderr, opts)
	return slog.New(handler)
}
