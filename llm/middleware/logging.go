package middleware

import (
	"fmt"
	"time"

	. "github.com/flanksource/commons-db/llm/types"
	"github.com/flanksource/commons/logger"
)

// LogConfig holds configuration for logging middleware
type LogConfig struct {
	Logger           logger.Logger   // Custom logger (optional, defaults to logger.StandardLogger())
	Level            logger.LogLevel // Minimum log level (default: Info)
	TruncatePrompt   int             // Truncate prompts longer than this (0 = no truncation)
	TruncateResponse int             // Truncate responses longer than this (0 = no truncation)
	RedactSensitive  bool            // Enable sensitive data redaction
	LogRequestBody   bool            // Log full request details (default: true)
	LogResponseBody  bool            // Log full response details (default: true)
}

// DefaultLogConfig returns the default logging configuration
func DefaultLogConfig() LogConfig {
	return LogConfig{
		Logger:           logger.StandardLogger(),
		Level:            logger.Info,
		TruncatePrompt:   500,
		TruncateResponse: 500,
		RedactSensitive:  false,
		LogRequestBody:   true,
		LogResponseBody:  true,
	}
}

// loggingProvider wraps a Provider with logging capabilities
type loggingProvider struct {
	logger.Logger
	provider Provider
	config   LogConfig
}

// newLoggingProvider creates a new logging middleware
func newLoggingProvider(provider Provider, config LogConfig) Provider {
	if config.Logger == nil {
		config.Logger = logger.StandardLogger()
	}
	return &loggingProvider{
		Logger:   config.Logger,
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

	if l.IsTraceEnabled() {
		l.Tracef(req.Pretty().ANSI())
	} else if l.IsDebugEnabled() {
		l.Debugf(req.PrettShort().ANSI())
	}

	resp, err := l.provider.Execute(sess, req)
	duration := time.Since(startTime)

	if err != nil {
		l.Errorf("[%s/%s] request failed after %s: %v", l.provider.GetBackend(), req.Model, duration.Round(time.Millisecond), err)
		return resp, err
	}

	if l.IsTraceEnabled() {
		l.Tracef(resp.Pretty().ANSI())
	} else if l.IsDebugEnabled() {
		l.Debugf(resp.PrettyShort().ANSI())
	}

	model := resp.Model
	if model == "" {
		model = req.Model
	}
	if resp.Text == "" && resp.StructuredData == nil {
		l.Warnf("[%s/%s] empty response after %s (in=%d out=%d)", l.provider.GetBackend(), model, duration.Round(time.Millisecond), resp.InputTokens, resp.OutputTokens)
	} else {
		l.Infof("[%s/%s] %s (in=%d out=%d%s)", l.provider.GetBackend(), model, duration.Round(time.Millisecond), resp.InputTokens, resp.OutputTokens, formatCost(sess))
	}

	return resp, nil
}

func formatCost(sess *Session) string {
	if sess == nil {
		return ""
	}
	cost := sess.Costs.Sum().TotalCost()
	if cost <= 0 {
		return ""
	}
	return fmt.Sprintf(" $%.4f", cost)
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
func WithLoggerAndLevel(log logger.Logger, level logger.LogLevel) Option {
	config := DefaultLogConfig()
	config.Logger = log
	config.Level = level
	return WithLogging(config)
}
