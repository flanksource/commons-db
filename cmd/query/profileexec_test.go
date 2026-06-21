package main

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// execMock is a query.Provider that echoes a fixed row set and records the query
// it was asked to run (so param templating can be asserted).
type execMock struct {
	rows []query.Row
	last query.ProviderRequest
}

func (m *execMock) Type() string { return "exec-mock" }
func (m *execMock) Execute(_ dbcontext.Context, req query.ProviderRequest) ([]query.Row, error) {
	m.last = req
	return m.rows, nil
}

func newExecTest(t *testing.T, p query.Profile) (*execHandler, *nextMarker, *execMock) {
	t.Helper()
	mock := &execMock{rows: []query.Row{{"id": 1}, {"id": 2}}}
	query.RegisterProvider(mock)

	store, err := NewProfileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	if err := store.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	next := &nextMarker{}
	return newExecHandler("/api/v1", dbcontext.New(), store, next), next, mock
}

func execProfile(name string) query.Profile {
	return query.Profile{
		Name:     name,
		Provider: query.ProviderConfig{Type: "exec-mock"},
		Query:    "select * where region = '{{.params.region}}'",
		Params:   []query.ParamDef{{Name: "region", Type: query.ParamTypeEnum, Options: []string{"US", "EU"}}},
	}
}

func TestExecHandlerExecutesProfileWithParams(t *testing.T) {
	h, next, mock := newExecTest(t, execProfile("activities"))

	rec := get(h, "/api/v1/profile/activities?region=EU", "")
	if next.hit {
		t.Fatal("expected exec handler to serve, not delegate")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode rows: %v; body=%s", err, rec.Body.String())
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if mock.last.Query != "select * where region = 'EU'" {
		t.Fatalf("param not templated into query: %q", mock.last.Query)
	}
}

func TestExecHandlerRejectsInvalidParam(t *testing.T) {
	h, _, _ := newExecTest(t, execProfile("activities"))
	rec := get(h, "/api/v1/profile/activities?region=MARS", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an invalid enum value", rec.Code)
	}
}

func TestExecHandlerDelegatesSchemaRequest(t *testing.T) {
	h, next, _ := newExecTest(t, execProfile("activities"))
	_ = get(h, "/api/v1/profile/activities", SchemaContentType)
	if !next.hit {
		t.Fatal("expected schema request to be delegated to next")
	}
}

func TestExecHandlerDelegatesListAndOtherPaths(t *testing.T) {
	for _, path := range []string{"/api/v1/profile", "/api/v1/connection", "/api/v1/profile/a/b"} {
		h, next, _ := newExecTest(t, execProfile("activities"))
		_ = get(h, path, "")
		if !next.hit {
			t.Fatalf("expected delegation for %q", path)
		}
	}
}

func TestExecHandlerMissingProfile(t *testing.T) {
	h, _, _ := newExecTest(t, execProfile("activities"))
	rec := get(h, "/api/v1/profile/nope", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

var _ = io.Discard
