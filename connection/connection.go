package connection

import (
	gocontext "context"
	"fmt"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
)

type ConnectionContext interface {
	gocontext.Context
	HydrateConnectionByURL(connectionName string) (*models.Connection, error)
	GetEnvValueFromCache(env types.EnvVar, namespace string) (string, error)
	GetNamespace() string
}

func Get(ctx context.Context, connectionName string) (*models.Connection, error) {
	connection, err := context.FindConnectionByURL(ctx, connectionName)
	if err != nil {
		return nil, err
	} else if connection == nil {
		return nil, fmt.Errorf("connection (%s) not found", connectionName)
	}

	return context.HydrateConnection(ctx, connection)
}
