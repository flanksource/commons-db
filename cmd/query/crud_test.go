package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flanksource/clicky/rpc"
)

// httpCtx mirrors what the clicky executor does on the HTTP path: it stashes the
// originating request (carrying the raw nested JSON body) on the context so the
// entity's context-aware Create/Update handlers can read it via nestedBody.
func httpCtx(t *testing.T, body any) context.Context {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/profile", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	return rpc.ContextWithRequest(context.Background(), req)
}

func TestNestedBodyReadsWrappedRequest(t *testing.T) {
	ctx := httpCtx(t, map[string]any{"name": "x", "properties": map[string]any{"sslmode": "disable"}})

	got, err := nestedBody(ctx, map[string]any{"name": "flattened"})
	if err != nil {
		t.Fatalf("nestedBody: %v", err)
	}
	props, ok := got["properties"].(map[string]any)
	if !ok || props["sslmode"] != "disable" {
		t.Fatalf("wrapped nested body not read: %+v", got)
	}
}

func TestNestedBodyFallsBackToFlattenedOnCLI(t *testing.T) {
	fallback := map[string]any{"name": "from-flags"}
	got, err := nestedBody(context.Background(), fallback)
	if err != nil {
		t.Fatalf("nestedBody: %v", err)
	}
	if got["name"] != "from-flags" {
		t.Fatalf("CLI path should use the flattened fallback, got %+v", got)
	}
}

// TestConnectionCreateBodyPreservesProperties guards the reason connections moved
// onto the context-aware create handler: the nested `properties` map must survive
// (the executor would otherwise stringify it).
func TestConnectionCreateBodyPreservesProperties(t *testing.T) {
	ctx := httpCtx(t, map[string]any{
		"name": "pg", "type": "postgres",
		"properties": map[string]any{"sslmode": "disable"},
	})

	body, err := nestedBody(ctx, nil)
	if err != nil {
		t.Fatalf("nestedBody: %v", err)
	}
	c, err := connectionFromBody(body)
	if err != nil {
		t.Fatalf("connectionFromBody: %v", err)
	}
	if c.Properties["sslmode"] != "disable" {
		t.Fatalf("nested properties lost: %+v", c.Properties)
	}
}

// TestSaveProfilePreservesNestedBody drives the profile entity's create handler
// over a simulated HTTP request and asserts the nested provider/options, params
// and columns round-trip through the store (the assertions the old writeHandler
// guarded, now served by the clicky entity).
func TestSaveProfilePreservesNestedBody(t *testing.T) {
	store, err := NewProfileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}

	ctx := httpCtx(t, map[string]any{
		"profile":  "Sales",
		"provider": map[string]any{"type": "postgres", "options": map[string]any{"url": "postgres://h/d"}},
		"query":    "select 1",
		"params":   []map[string]any{{"name": "region", "type": "enum", "options": []string{"US", "EU"}}},
		"columns":  []map[string]any{{"name": "n", "type": "number"}},
	})

	if _, err := saveProfile(store, ctx, map[string]any{}, ""); err != nil {
		t.Fatalf("saveProfile: %v", err)
	}

	got, err := store.Get("Sales")
	if err != nil {
		t.Fatalf("profile not saved: %v", err)
	}
	if got.Provider.Type != "postgres" || got.Provider.Options["url"] != "postgres://h/d" {
		t.Fatalf("nested provider lost: %+v", got.Provider)
	}
	if len(got.Params) != 1 || got.Params[0].Name != "region" {
		t.Fatalf("nested params lost: %+v", got.Params)
	}
	if len(got.Columns) != 1 || got.Columns[0].Name != "n" {
		t.Fatalf("nested columns lost: %+v", got.Columns)
	}
}

// TestSaveProfileUpdateUsesPathID verifies a body that omits the name is saved
// under the id supplied by the path (the update route's {id}).
func TestSaveProfileUpdateUsesPathID(t *testing.T) {
	store, err := NewProfileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	if err := store.Save(sampleProfile("Existing")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx := httpCtx(t, map[string]any{"provider": map[string]any{"type": "sql"}, "query": "select 2"})
	if _, err := saveProfile(store, ctx, map[string]any{}, "Existing"); err != nil {
		t.Fatalf("saveProfile: %v", err)
	}

	got, err := store.Get("Existing")
	if err != nil || got.Query != "select 2" {
		t.Fatalf("update not applied: %+v err=%v", got, err)
	}
}
