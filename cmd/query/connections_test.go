package main

import "testing"

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
