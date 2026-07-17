package connection

import (
	"testing"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
)

type openTelemetryHydrator struct {
	connection *models.Connection
	requested  string
}

func (h *openTelemetryHydrator) HydrateConnectionByURL(connection string) (*models.Connection, error) {
	h.requested = connection
	return h.connection, nil
}

func TestOpenTelemetryResolvesNestedOpenSearchConnection(t *testing.T) {
	outer, err := NewOpenTelemetry(&models.Connection{
		Name: "traces", Type: models.ConnectionTypeOpenTelemetry,
		Properties: types.JSONStringMap{"connection": "connection://OS"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hydrator := &openTelemetryHydrator{connection: &models.Connection{Name: "OS", Type: models.ConnectionTypeOpenSearch}}
	nested, err := outer.ResolveOpenSearch(hydrator)
	if err != nil {
		t.Fatal(err)
	}
	if hydrator.requested != "connection://OS" || nested.Name != "OS" {
		t.Fatalf("unexpected nested resolution: requested=%q connection=%+v", hydrator.requested, nested)
	}
}

func TestOpenTelemetryRejectsWrongNestedConnectionType(t *testing.T) {
	outer, err := NewOpenTelemetry(&models.Connection{
		Name: "traces", Type: models.ConnectionTypeOpenTelemetry,
		Properties: types.JSONStringMap{"connection": "connection://db"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = outer.ResolveOpenSearch(&openTelemetryHydrator{connection: &models.Connection{Name: "db", Type: models.ConnectionTypePostgres}})
	if err == nil {
		t.Fatal("expected wrong nested connection type to fail")
	}
}
