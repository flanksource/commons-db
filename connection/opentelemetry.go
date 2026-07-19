package connection

import (
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/models"
)

type OpenTelemetry struct {
	Name       string
	Connection string
}

type connectionHydrator interface {
	HydrateConnectionByURL(string) (*models.Connection, error)
}

func NewOpenTelemetry(connection *models.Connection) (OpenTelemetry, error) {
	if connection == nil {
		return OpenTelemetry{}, fmt.Errorf("opentelemetry connection is required")
	}
	if connection.Type != models.ConnectionTypeOpenTelemetry {
		return OpenTelemetry{}, fmt.Errorf("connection %q has type %q, expected %q", connection.Name, connection.Type, models.ConnectionTypeOpenTelemetry)
	}
	nested := strings.TrimSpace(connection.Properties["connection"])
	if nested == "" {
		return OpenTelemetry{}, fmt.Errorf("opentelemetry connection %q is missing properties.connection", connection.Name)
	}
	if nested == "connection://"+connection.Name {
		return OpenTelemetry{}, fmt.Errorf("opentelemetry connection %q cannot reference itself", connection.Name)
	}
	return OpenTelemetry{Name: connection.Name, Connection: nested}, nil
}

func (connection OpenTelemetry) ResolveOpenSearch(ctx connectionHydrator) (*models.Connection, error) {
	nested, err := ctx.HydrateConnectionByURL(connection.Connection)
	if err != nil {
		return nil, fmt.Errorf("resolve nested OpenSearch connection %q: %w", connection.Connection, err)
	}
	if nested == nil {
		return nil, fmt.Errorf("nested OpenSearch connection %q not found", connection.Connection)
	}
	if nested.Type != models.ConnectionTypeOpenSearch {
		return nil, fmt.Errorf("nested connection %q has type %q, expected %q", nested.Name, nested.Type, models.ConnectionTypeOpenSearch)
	}
	return nested, nil
}
