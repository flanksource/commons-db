package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newWriteTest(t *testing.T) (*writeHandler, *nextMarker, *ProfileStore) {
	t.Helper()
	store, err := NewProfileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	next := &nextMarker{}
	return newWriteHandler("/api/v1", nil, store, next), next, store
}

func doJSON(h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestWriteHandlerCreatesProfile(t *testing.T) {
	h, next, store := newWriteTest(t)

	body := map[string]any{
		"profile":  "Sales",
		"provider": map[string]any{"type": "postgres", "options": map[string]any{"url": "postgres://h/d"}},
		"query":    "select 1",
		"params":   []map[string]any{{"name": "region", "type": "enum", "options": []string{"US", "EU"}}},
		"columns":  []map[string]any{{"name": "n", "type": "number"}},
	}
	rec := doJSON(h, http.MethodPost, "/api/v1/profile", body)
	if next.hit {
		t.Fatal("expected write handler to serve POST, not delegate")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	got, err := store.Get("Sales")
	if err != nil {
		t.Fatalf("profile not saved: %v", err)
	}
	// nested structures must survive the round-trip (the reason writes bypass clicky)
	if got.Provider.Type != "postgres" || got.Provider.Options["url"] != "postgres://h/d" {
		t.Fatalf("nested provider lost: %+v", got.Provider)
	}
	if len(got.Params) != 1 || got.Params[0].Name != "region" {
		t.Fatalf("nested params lost: %+v", got.Params)
	}
}

func TestWriteHandlerUpdateUsesPathIDForName(t *testing.T) {
	h, _, store := newWriteTest(t)
	if err := store.Save(sampleProfile("Existing")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// body omits the name; the path id supplies it
	body := map[string]any{"provider": map[string]any{"type": "sql"}, "query": "select 2"}
	rec := doJSON(h, http.MethodPut, "/api/v1/profile/Existing", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, err := store.Get("Existing")
	if err != nil || got.Query != "select 2" {
		t.Fatalf("update not applied: %+v err=%v", got, err)
	}
}

func TestWriteHandlerDeletesProfile(t *testing.T) {
	h, _, store := newWriteTest(t)
	if err := store.Save(sampleProfile("Doomed")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec := doJSON(h, http.MethodDelete, "/api/v1/profile/Doomed", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if _, err := store.Get("Doomed"); err == nil {
		t.Fatal("profile should be deleted")
	}
}

func TestWriteHandlerDelegatesReads(t *testing.T) {
	h, next, _ := newWriteTest(t)
	_ = doJSON(h, http.MethodGet, "/api/v1/profile", nil)
	if !next.hit {
		t.Fatal("GET should delegate to next")
	}
}

func TestWriteHandlerDelegatesUnknownResource(t *testing.T) {
	h, next, _ := newWriteTest(t)
	_ = doJSON(h, http.MethodPost, "/api/v1/widget", map[string]any{"x": 1})
	if !next.hit {
		t.Fatal("unknown resource POST should delegate")
	}
}
