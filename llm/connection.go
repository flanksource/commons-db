package llm

import (
	gocontext "context"
	"fmt"

	"github.com/flanksource/commons-db/connection"
	dutyctx "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/types"
)

// resolveConnection resolves a named connection from the duty/connection registry.
func resolveConnection(ctx gocontext.Context, name string) (*Connection, error) {
	// Convert to duty context
	dutyContext, ok := ctx.(dutyctx.Context)
	if !ok {
		return nil, fmt.Errorf("context must be a duty context")
	}

	// Get connection from database
	conn, err := connection.Get(dutyContext, name)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrConnectionNotFound, name)
	}
	if conn == nil {
		return nil, fmt.Errorf("%w: %s", ErrConnectionNotFound, name)
	}

	// Map connection type to LLM backend
	backend, err := mapConnectionTypeToBackend(conn.Type)
	if err != nil {
		return nil, err
	}

	// Build Connection with types.HTTP embedded
	llmConn := &Connection{
		Backend: backend,
		Model:   conn.Properties["model"],
		HTTP: types.HTTP{
			URL: types.EnvVar{
				ValueStatic: conn.URL,
			},
			Bearer: types.EnvVar{
				ValueStatic: conn.Password,
			},
		},
	}

	return llmConn, nil
}

// mapConnectionTypeToBackend maps a connection type string to an LLMBackend.
func mapConnectionTypeToBackend(connType string) (LLMBackend, error) {
	switch connType {
	case "openai":
		return LLMBackendOpenAI, nil
	case "anthropic":
		return LLMBackendAnthropic, nil
	case "gemini":
		return LLMBackendGemini, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrInvalidProvider, connType)
	}
}
