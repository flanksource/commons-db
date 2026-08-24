package connection

import (
	gocontext "context"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
)

// stubContext is a ConnectionContext backed by one in-memory connection row.
// EnvVar resolution is the identity, so a spec asserts on the mapping from the
// stored row onto the typed connection rather than on secret lookup.
type stubContext struct {
	gocontext.Context

	connection *models.Connection
}

func newStubContext(connection *models.Connection) stubContext {
	return stubContext{Context: gocontext.Background(), connection: connection}
}

func (s stubContext) HydrateConnectionByURL(string) (*models.Connection, error) {
	return s.connection, nil
}

func (s stubContext) GetEnvValueFromCache(env types.EnvVar, _ string) (string, error) {
	return env.ValueStatic, nil
}

func (s stubContext) GetNamespace() string { return "" }
