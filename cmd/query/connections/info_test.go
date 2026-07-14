package connections

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/types"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestConnectionInfoEndpointIsSafeAndNonBlocking(t *testing.T) {
	gdb := connectionInfoTestDB(t)
	connection := models.Connection{
		ID: uuid.New(), Name: "api", Namespace: "mission-control", Type: models.ConnectionTypeHTTP,
		URL: "https://operator:embedded@example.test/api?token=top-secret", Username: "resolved-user",
		Password: "password-secret", Certificate: "certificate-secret",
		Properties: types.JSONStringMap{"bearer": "bearer-secret"},
	}
	if err := gdb.Create(&connection).Error; err != nil {
		t.Fatal(err)
	}
	ctx := dbcontext.NewContext(context.Background()).WithDB(gdb, nil)
	handler := newConnectionBrowserHandler("/api/v1", ctx, http.NotFoundHandler())

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/connection/"+connection.ID.String()+"/info", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("info status = %d: %s", recorder.Code, recorder.Body.String())
	}
	for _, secret := range []string{"top-secret", "embedded", "password-secret", "certificate-secret", "bearer-secret"} {
		if strings.Contains(recorder.Body.String(), secret) {
			t.Errorf("response exposed %q: %s", secret, recorder.Body.String())
		}
	}
	var got connectionInfo
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Connection.ResolvedUsername != "resolved-user" || !got.Connection.Password.Resolved || !got.Connection.Certificate.Resolved {
		t.Fatalf("resolved connection summary = %#v", got.Connection)
	}
	if got.Server.Status != "unavailable" {
		t.Fatalf("HTTP server discovery status = %q, want unavailable", got.Server.Status)
	}
}

func TestCloneConnectionDoesNotShareProperties(t *testing.T) {
	original := &models.Connection{Properties: types.JSONStringMap{"token": "configured"}}
	clone := cloneConnection(original)
	clone.Properties["token"] = "resolved"
	if original.Properties["token"] != "configured" {
		t.Fatalf("clone mutated original properties: %#v", original.Properties)
	}
}

func TestDiscoverOpenSearchAndPrometheus(t *testing.T) {
	tests := []struct {
		name           string
		connectionType string
		path           string
		body           string
		version        string
		product        string
	}{
		{
			name: "opensearch", connectionType: models.ConnectionTypeOpenSearch, path: "/",
			body:    `{"name":"search-0","cluster_name":"logs","version":{"distribution":"opensearch","number":"2.19.1","lucene_version":"9.12.0"}}`,
			version: "2.19.1", product: "opensearch",
		},
		{
			name: "prometheus", connectionType: models.ConnectionTypePrometheus, path: "/api/v1/status/buildinfo",
			body:    `{"status":"success","data":{"version":"3.5.0","revision":"abc","branch":"main","buildDate":"today","goVersion":"go1.24"}}`,
			version: "3.5.0", product: "Prometheus",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.path {
					t.Fatalf("metadata path = %q, want %q", r.URL.Path, tt.path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			ctx := dbcontext.NewContext(context.Background())
			got := discoverServer(context.Background(), ctx, &models.Connection{Type: tt.connectionType, URL: server.URL})
			if got.Status != "available" || got.Version != tt.version || got.Product != tt.product {
				t.Fatalf("discovery = %#v", got)
			}
		})
	}
}

func TestParseRedisInfo(t *testing.T) {
	got := parseRedisInfo("# Server\r\nredis_version:7.2.4\r\nredis_mode:standalone\r\nos:Linux\r\narch_bits:64\r\n")
	if got["redis_version"] != "7.2.4" || got["redis_mode"] != "standalone" || got["arch_bits"] != "64" {
		t.Fatalf("parsed info = %#v", got)
	}
}

func TestSanitizeConnectionError(t *testing.T) {
	connection := &models.Connection{
		URL: "postgres://user:url-secret@localhost/db", Password: "field-secret", Certificate: "cert-secret",
		Properties: types.JSONStringMap{"token": "token-secret"},
	}
	got := sanitizeConnectionError(
		context.Canceled,
		connection,
	)
	if got != context.Canceled.Error() {
		t.Fatalf("unexpected sanitized error: %q", got)
	}
	got = sanitizeConnectionError(
		&testConnectionInfoError{"failed " + connection.URL + " field-secret cert-secret token-secret"},
		connection,
	)
	for _, secret := range []string{"url-secret", "field-secret", "cert-secret", "token-secret"} {
		if strings.Contains(got, secret) {
			t.Errorf("sanitized error exposed %q: %s", secret, got)
		}
	}
}

type testConnectionInfoError struct{ message string }

func (e *testConnectionInfoError) Error() string { return e.message }

func connectionInfoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`CREATE TABLE connections (
        id TEXT PRIMARY KEY, name TEXT, namespace TEXT, source TEXT, type TEXT,
        url TEXT, username TEXT, password TEXT, properties TEXT, certificate TEXT,
        insecure_tls NUMERIC, created_at DATETIME, updated_at DATETIME, created_by TEXT
    )`).Error; err != nil {
		t.Fatal(err)
	}
	return gdb
}
