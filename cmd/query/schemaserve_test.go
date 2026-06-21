package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// nextMarker is a sentinel handler that records whether it was reached.
type nextMarker struct{ hit bool }

func (n *nextMarker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n.hit = true
	w.WriteHeader(http.StatusTeapot)
}

func newTestHandler(t *testing.T) (*schemaHandler, *nextMarker) {
	t.Helper()
	store, err := NewProfileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	if err := store.Save(sampleProfile("Sales Report")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	next := &nextMarker{}
	return newSchemaHandler("/api/v1", store, next), next
}

func get(h http.Handler, path, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSchemaHandlerServesConnectionSchema(t *testing.T) {
	h, next := newTestHandler(t)
	rec := get(h, "/api/v1/connection", SchemaContentType)

	if next.hit {
		t.Fatal("expected schema handler to serve, not delegate")
	}
	if ct := rec.Header().Get("Content-Type"); ct != SchemaContentType {
		t.Fatalf("content-type = %q, want %q", ct, SchemaContentType)
	}
	body, _ := io.ReadAll(rec.Body)
	if !isSchemaDoc(body) {
		t.Fatalf("response is not a JSON schema: %s", body)
	}
}

func TestSchemaHandlerServesProfileSetupSchema(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := get(h, "/api/v1/profile", SchemaContentType)
	body, _ := io.ReadAll(rec.Body)
	if !isSchemaDoc(body) {
		t.Fatalf("expected profile-setup schema, got: %s", body)
	}
}

func TestSchemaHandlerServesPerProfileSchema(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := get(h, "/api/v1/profile/Sales%20Report", SchemaContentType)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body, _ := io.ReadAll(rec.Body)
	if !isSchemaDoc(body) {
		t.Fatalf("expected per-profile schema, got: %s", body)
	}
}

func TestSchemaHandlerDelegatesWithoutSchemaAccept(t *testing.T) {
	h, next := newTestHandler(t)
	rec := get(h, "/api/v1/connection", "application/json")
	if !next.hit {
		t.Fatal("expected delegation to next handler for non-schema Accept")
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418 (from next)", rec.Code)
	}
}

func TestSchemaHandlerUnknownPathDelegates(t *testing.T) {
	h, next := newTestHandler(t)
	_ = get(h, "/api/v1/widget", SchemaContentType)
	if !next.hit {
		t.Fatal("expected delegation for an unknown resource path")
	}
}

func TestSchemaHandlerMissingProfileErrors(t *testing.T) {
	h, _ := newTestHandler(t)
	rec := get(h, "/api/v1/profile/nope", SchemaContentType)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for a missing profile", rec.Code)
	}
}
