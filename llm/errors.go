package llm

import "errors"

var (
	// ErrConnectionNotFound is returned when a named connection doesn't exist in the registry.
	ErrConnectionNotFound = errors.New("connection not found")

	// ErrMissingAPIKey is returned when a connection has no API key (password field).
	ErrMissingAPIKey = errors.New("connection missing API key")

	// ErrInvalidProvider is returned when the connection type doesn't map to a known provider.
	ErrInvalidProvider = errors.New("invalid or unknown provider type")

	// ErrMissingPrompt is returned when Execute() is called without a prompt.
	ErrMissingPrompt = errors.New("prompt is required")

	// ErrMissingConnection is returned when Execute() is called without a connection name.
	ErrMissingConnection = errors.New("connection name is required")

	// ErrTimeout is returned when a request exceeds the configured timeout.
	ErrTimeout = errors.New("request timeout exceeded")

	// ErrSchemaValidation is returned when structured output doesn't match the schema.
	ErrSchemaValidation = errors.New("response failed schema validation")

	// ErrInvalidMaxTokens is returned when max tokens is <= 0.
	ErrInvalidMaxTokens = errors.New("max tokens must be greater than 0")

	// ErrInvalidTimeout is returned when timeout is <= 0.
	ErrInvalidTimeout = errors.New("timeout must be greater than 0")
)
