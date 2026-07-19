package connections

import (
	"testing"

	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	"github.com/google/uuid"
)

// TestConnectionFromBodyIgnoresID guards the update path: the body carries the id
// (a uuid or a name) to address the row, but it must not be parsed into the uuid
// ID field — a name like "pg-e2e" would otherwise fail with "invalid UUID length".
func TestConnectionFromBodyIgnoresID(t *testing.T) {
	c, err := connectionFromBody(map[string]any{"id": "pg-e2e", "name": "db", "type": "postgres"})
	if err != nil {
		t.Fatalf("connectionFromBody with a name id should not error: %v", err)
	}
	if c.ID != uuid.Nil {
		t.Fatalf("body id must be ignored, got %v", c.ID)
	}
}

func TestConnectionFromBody(t *testing.T) {
	c, err := connectionFromBody(map[string]any{
		"name": "db", "type": "postgres", "url": "postgres://h/d", "password": "secret",
	})
	if err != nil {
		t.Fatalf("connectionFromBody: %v", err)
	}
	if c.Name != "db" || c.Type != "postgres" || c.Password != "secret" {
		t.Fatalf("decoded connection mismatch: %+v", c)
	}
}

func TestConnectionFromBodyRequiresNameAndType(t *testing.T) {
	if _, err := connectionFromBody(map[string]any{"type": "postgres"}); err == nil {
		t.Fatal("expected error when name is missing")
	}
	if _, err := connectionFromBody(map[string]any{"name": "db"}); err == nil {
		t.Fatal("expected error when type is missing")
	}
}

func TestValidateOpenTelemetryRequiresNestedOpenSearchConnection(t *testing.T) {
	database := connectionInfoTestDB(t)
	wrong := models.Connection{ID: uuid.New(), Name: "OS", Type: models.ConnectionTypeHTTP}
	search := models.Connection{ID: uuid.New(), Name: "OS", Namespace: "team", Type: models.ConnectionTypeOpenSearch}
	if err := database.Create(&wrong).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&search).Error; err != nil {
		t.Fatal(err)
	}
	candidate := &models.Connection{
		Name: "traces", Type: models.ConnectionTypeOpenTelemetry,
		Properties: types.JSONStringMap{"connection": "connection://team/OS"},
	}
	if err := validateNestedConnection(database, candidate); err != nil {
		t.Fatal(err)
	}
	candidate.Properties["connection"] = "connection://traces"
	if err := validateNestedConnection(database, candidate); err == nil {
		t.Fatal("expected self-referencing OpenTelemetry connection to fail")
	}
	candidate.Properties["connection"] = "https://example.test/OS"
	if err := validateNestedConnection(database, candidate); err == nil {
		t.Fatal("expected an HTTP URL ending in the unrelated connection name to fail")
	}
}

func TestApplyConnectionUpdatePreservesSecretWhenBlank(t *testing.T) {
	existing, _ := connectionFromBody(map[string]any{"name": "db", "type": "postgres", "password": "old"})
	incoming, _ := connectionFromBody(map[string]any{"name": "db2", "type": "mysql"}) // no password

	applyConnectionUpdate(existing, incoming)
	if existing.Name != "db2" || existing.Type != "mysql" {
		t.Fatalf("editable fields not updated: %+v", existing)
	}
	if existing.Password != "old" {
		t.Fatalf("blank incoming password should preserve stored secret, got %q", existing.Password)
	}

	incoming2, _ := connectionFromBody(map[string]any{"name": "db2", "type": "mysql", "password": "new"})
	applyConnectionUpdate(existing, incoming2)
	if existing.Password != "new" {
		t.Fatalf("non-blank password should overwrite, got %q", existing.Password)
	}
}

func TestRedactConnection(t *testing.T) {
	c, _ := connectionFromBody(map[string]any{"name": "db", "type": "postgres", "password": "secret"})
	c.Certificate = "cert"
	redactConnection(c)
	if c.Password != "" || c.Certificate != "" {
		t.Fatalf("secrets not redacted: %+v", c)
	}
}

func TestRedactConnectionInfersLegacyHTTPAuthentication(t *testing.T) {
	c := &models.Connection{
		Type: models.ConnectionTypeOpenSearch, Username: "admin", Password: "secret",
	}
	redactConnection(c)
	if c.Properties["authType"] != "basic" {
		t.Fatalf("authType = %q, want basic", c.Properties["authType"])
	}
	if c.Password != "" {
		t.Fatal("password was not redacted")
	}
}
