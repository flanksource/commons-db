package main

import (
	"testing"

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
