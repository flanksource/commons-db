package profiles

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

// rowsProvider is a registered provider that returns a fixed set of rows, so
// the replay and reconcile actions can be exercised without a backend.
type rowsProvider struct {
	name string
	rows []query.Row
}

func (p rowsProvider) Type() string { return p.name }
func (p rowsProvider) Execute(_ dbcontext.Context, _ query.ProviderRequest) ([]query.Row, error) {
	return p.rows, nil
}

// serviceOver builds a Service backed by a temp file store holding profiles.
func serviceOver(t *testing.T, profiles ...query.Profile) *Service {
	t.Helper()
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	for _, profile := range profiles {
		if err := store.Save(context.Background(), profile); err != nil {
			t.Fatalf("Save %q: %v", profile.Name, err)
		}
	}
	service, err := New(Options{
		Store:      func() (Store, error) { return store, nil },
		Context:    func() dbcontext.Context { return dbcontext.New() },
		DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return service
}

// ledgerReplayProfile returns a profile whose rows can be replayed as POSTs to
// target, with a payload the CEL body expression reads.
func ledgerReplayProfile(t *testing.T, name, providerType, target string, rows []query.Row) query.Profile {
	t.Helper()
	query.RegisterProvider(rowsProvider{name: providerType, rows: rows})
	return query.Profile{
		Name:     name,
		Provider: query.ProviderConfig{Type: providerType},
		Columns:  []query.ColumnDef{{Name: "id"}, {Name: "path"}},
		Replay: &query.ReplaySpec{
			Kind:    query.ReplayKindHTTP,
			Target:  connection.HTTPConnection{URL: target},
			Method:  `"POST"`,
			URL:     `path`,
			Body:    `payload`,
			Headers: map[string]string{"X-Id": `id`},
		},
	}
}

func TestReplayPreviewsWithoutSending(t *testing.T) {
	var sent int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sent++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	profile := ledgerReplayProfile(t, "preview-only", "replay-preview-only", server.URL,
		[]query.Row{{"id": "1", "path": "/ledger", "payload": "body-1"}})
	service := serviceOver(t, profile)

	result, err := service.Replay(context.Background(), profile.Name, ReplayFlags{})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if result.Executed != nil {
		t.Fatalf("preview returned an execution result: %+v", result.Executed)
	}
	if sent != 0 {
		t.Fatalf("preview sent %d requests; it must send none", sent)
	}
	if want := server.URL + "/ledger"; result.Preview.URL != want {
		t.Errorf("preview URL = %q, want %q", result.Preview.URL, want)
	}
	if result.Preview.BodyPreview != "body-1" {
		t.Errorf("preview body = %q, want body-1", result.Preview.BodyPreview)
	}
}

func TestReplayExecuteSendsTheRequest(t *testing.T) {
	var gotPath, gotID, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotPath, gotID, gotBody = r.URL.Path, r.Header.Get("X-Id"), string(body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	profile := ledgerReplayProfile(t, "execute", "replay-execute", server.URL,
		[]query.Row{{"id": "42", "path": "/ledger", "payload": "body-42"}})
	service := serviceOver(t, profile)

	result, err := service.Replay(context.Background(), profile.Name, ReplayFlags{Execute: true})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if result.Executed == nil {
		t.Fatal("execute returned no execution result")
	}
	if result.Executed.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want %d", result.Executed.StatusCode, http.StatusAccepted)
	}
	if gotPath != "/ledger" || gotID != "42" || gotBody != "body-42" {
		t.Errorf("received path=%q id=%q body=%q", gotPath, gotID, gotBody)
	}
}

// The hash guard is the whole point of the two-step handshake: it must refuse
// to send a request that differs from the one the caller approved.
func TestReplayRefusesAStalePreviewHash(t *testing.T) {
	var sent int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sent++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	profile := ledgerReplayProfile(t, "stale", "replay-stale", server.URL,
		[]query.Row{{"id": "1", "path": "/ledger", "payload": "body-1"}})
	service := serviceOver(t, profile)

	_, err := service.Replay(context.Background(), profile.Name, ReplayFlags{
		Execute: true, Hash: "0000000000000000000000000000000000000000000000000000000000000000",
	})
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("err = %v, want a staleness error", err)
	}
	if sent != 0 {
		t.Fatalf("a stale preview still sent %d requests", sent)
	}
}

func TestReplayAcceptsAMatchingPreviewHash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	profile := ledgerReplayProfile(t, "fresh", "replay-fresh", server.URL,
		[]query.Row{{"id": "1", "path": "/ledger", "payload": "body-1"}})
	service := serviceOver(t, profile)

	preview, err := service.Replay(context.Background(), profile.Name, ReplayFlags{})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	result, err := service.Replay(context.Background(), profile.Name, ReplayFlags{Execute: true, Hash: preview.Preview.Hash})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Executed == nil {
		t.Fatal("a matching hash did not send the request")
	}
}

func TestReplaySelectsOneRowOfMany(t *testing.T) {
	profile := ledgerReplayProfile(t, "select", "replay-select", "https://api.example.test", []query.Row{
		{"id": "1", "path": "/a", "payload": "one"},
		{"id": "2", "path": "/b", "payload": "two"},
	})
	service := serviceOver(t, profile)

	result, err := service.Replay(context.Background(), profile.Name, ReplayFlags{Select: []string{"id=2"}})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if result.Preview.BodyPreview != "two" {
		t.Errorf("body = %q, want two", result.Preview.BodyPreview)
	}

	if _, err := service.Replay(context.Background(), profile.Name, ReplayFlags{}); err == nil ||
		!strings.Contains(err.Error(), "2 rows matched") {
		t.Fatalf("err = %v, want an ambiguity error", err)
	}
}

func TestReplayRejectsAProfileWithoutAReplayBlock(t *testing.T) {
	query.RegisterProvider(rowsProvider{name: "replay-missing", rows: []query.Row{{"id": "1"}}})
	profile := query.Profile{Name: "no-replay", Provider: query.ProviderConfig{Type: "replay-missing"}}
	service := serviceOver(t, profile)

	if _, err := service.Replay(context.Background(), profile.Name, ReplayFlags{}); err == nil ||
		!strings.Contains(err.Error(), "declares no replay block") {
		t.Fatalf("err = %v, want a missing-replay-block error", err)
	}
}

func TestReplayRejectsMalformedKeyValueFlags(t *testing.T) {
	profile := ledgerReplayProfile(t, "malformed", "replay-malformed", "https://api.example.test",
		[]query.Row{{"id": "1", "path": "/a", "payload": "one"}})
	service := serviceOver(t, profile)

	for flag, options := range map[string]ReplayFlags{
		"select": {Select: []string{"noequals"}},
		"header": {Header: []string{"=novalue"}},
		"param":  {Params: []string{"noequals"}},
	} {
		if _, err := service.Replay(context.Background(), profile.Name, options); err == nil ||
			!strings.Contains(err.Error(), flag) {
			t.Errorf("--%s: err = %v, want a key=value error", flag, err)
		}
	}
}

func TestReplayResultSerialisesPreviewAndExecution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	profile := ledgerReplayProfile(t, "json", "replay-json", server.URL,
		[]query.Row{{"id": "1", "path": "/ledger", "payload": "body-1"}})
	service := serviceOver(t, profile)

	result, err := service.Replay(context.Background(), profile.Name, ReplayFlags{Execute: true})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded struct {
		Preview  map[string]any `json:"preview"`
		Executed map[string]any `json:"executed"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Preview["hash"] == "" || decoded.Executed["statusCode"] == nil {
		t.Errorf("serialised result is missing the preview hash or the response status: %s", encoded)
	}
}
