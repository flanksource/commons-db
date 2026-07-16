package connections

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flanksource/commons/properties"
	"github.com/google/uuid"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
)

func privateProbeTestContext(t *testing.T) dbcontext.Context {
	t.Helper()
	properties.Set(allowPrivateConnectionProbeProperty, "true")
	ctx := dbcontext.NewContext(context.Background())
	ctx.ClearCache()
	t.Cleanup(func() {
		properties.Set(allowPrivateConnectionProbeProperty, "")
		ctx.ClearCache()
	})
	return ctx
}

func TestMaskedConnection(t *testing.T) {
	got := maskedConnection(&models.Connection{
		Type:        "postgres",
		Namespace:   "prod",
		URL:         "postgres://db.prod.svc.cluster.local:5432",
		Username:    "app",
		Password:    "supersecret",
		Certificate: "----BEGIN CERT----abcdefgh----END----",
	})
	if got.URL != "postgres://db.prod.svc.cluster.local:5432" || got.Username != "app" {
		t.Errorf("url/username should be preserved, got %+v", got)
	}
	if got.Password != "supe••••cret" {
		t.Errorf("password should be mid-masked, got %q", got.Password)
	}
	if got.Certificate == "" || got.Certificate == "----BEGIN CERT----abcdefgh----END----" {
		t.Errorf("certificate should be masked, got %q", got.Certificate)
	}
}

func TestMaskedConnectionRedactsEmbeddedCredentials(t *testing.T) {
	got := maskedConnection(&models.Connection{
		Type: "postgres",
		URL:  "postgres://app:supersecret@db.prod.svc.cluster.local:5432/app?sslmode=require&password=querysecret",
		Properties: map[string]string{
			"host":     "db.prod.svc.cluster.local",
			"password": "propertysecret",
		},
	})

	if strings.Contains(got.URL, "app:supersecret") || strings.Contains(got.URL, "querysecret") {
		t.Fatalf("url should be redacted, got %q", got.URL)
	}
	if got.URL != "postgres://db.prod.svc.cluster.local:5432/app?password=redacted&sslmode=require" {
		t.Errorf("redacted url = %q", got.URL)
	}
	if got.Properties["password"] == "propertysecret" {
		t.Errorf("sensitive properties should be masked, got %+v", got.Properties)
	}
}

func TestRedactConnectionURLKeyValueDSN(t *testing.T) {
	raw := "server=mssql.lab;user id=sa;password=YourStrong@Passw0rd;database=LAB_APP_QA;port=31433"
	got := redactConnectionURL(raw)
	if strings.Contains(got, "YourStrong@Passw0rd") || strings.Contains(got, "user id=sa") {
		t.Fatalf("DSN credentials should be redacted, got %q", got)
	}
	if !strings.Contains(got, "server=mssql.lab") || !strings.Contains(got, "port=31433") {
		t.Fatalf("non-sensitive DSN fields should be preserved, got %q", got)
	}
}

func TestRedactConnectionURLHostlessUserinfo(t *testing.T) {
	got := redactConnectionURL("https://user:secret@?token=secret-token")
	if strings.Contains(got, "user:secret") || strings.Contains(got, "secret-token") {
		t.Fatalf("URL userinfo and sensitive query values should be redacted, got %q", got)
	}
	if got != "https:?token=redacted" {
		t.Fatalf("redacted URL = %q", got)
	}
}

func TestDefaultPort(t *testing.T) {
	cases := map[string]string{"http": "80", "https": "443", "postgres": "5432", "redis": "6379", "weird": ""}
	for scheme, want := range cases {
		if got := defaultPort(scheme); got != want {
			t.Errorf("defaultPort(%q) = %q, want %q", scheme, got, want)
		}
	}
}

func TestDialTarget(t *testing.T) {
	cases := []struct {
		name     string
		url      string
		connType string
		wantHost string
		wantOK   bool
	}{
		{"url with port", "postgres://db.local:6543", "postgres", "db.local:6543", true},
		{"url default port", "postgres://db.local", "postgres", "db.local:5432", true},
		{"sqlserver url", "sqlserver://u:p@db.local?database=x", "sql_server", "db.local:1433", true},
		{
			name:     "ado key-value dsn",
			url:      "server=mssql.lab;user id=sa;password=YourStrong@Passw0rd;database=LAB_APP_QA;port=31433;trustServerCertificate=true",
			connType: "sql_server",
			wantHost: "mssql.lab:31433",
			wantOK:   true,
		},
		{"ado without port", "server=mssql.lab;database=x", "sql_server", "mssql.lab:1433", true},
		{"ado server,port", "data source=mssql.lab,31433;database=x", "sql_server", "mssql.lab:31433", true},
		{"ado tcp prefix and instance", "server=tcp:mssql.lab\\SQLEXPRESS,1433", "sql_server", "mssql.lab:1433", true},
		{"unparseable", "not a url or dsn", "sql_server", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, _, ok := dialTarget(tc.url, tc.connType)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if host != tc.wantHost {
				t.Errorf("host = %q, want %q", host, tc.wantHost)
			}
		})
	}
}

func TestTestConnectionADOReachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}

	ctx := privateProbeTestContext(t)
	dsn := "server=" + host + ";port=" + port + ";database=x;trustServerCertificate=true"
	res := testConnection(ctx, &models.Connection{Type: "sql_server", URL: dsn})
	if !res.OK {
		t.Fatalf("expected ADO DSN host reachable, got %+v", res)
	}
}

func TestTestConnectionHTTPReachable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	ctx := privateProbeTestContext(t)
	res := testConnection(ctx, &models.Connection{Type: "http", URL: ts.URL})
	if !res.OK {
		t.Fatalf("expected reachable, got %+v", res)
	}
	if res.Message != "HTTP 200 OK" {
		t.Errorf("message = %q, want HTTP 200 OK", res.Message)
	}
}

func TestTestConnectionHTTPRedactsURL(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	u := strings.Replace(ts.URL, "http://", "http://user:pass@", 1) + "?token=secret-token"
	ctx := privateProbeTestContext(t)
	res := testConnection(ctx, &models.Connection{Type: "http", URL: u})
	if !res.OK {
		t.Fatalf("expected reachable, got %+v", res)
	}
	if strings.Contains(res.URL, "user:pass") || strings.Contains(res.URL, "secret-token") {
		t.Fatalf("test result URL should be redacted, got %+v", res)
	}
}

func TestTestConnectionHTTPSReachableWithInsecureTLS(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	ctx := privateProbeTestContext(t)
	res := testConnection(ctx, &models.Connection{Type: "https", URL: ts.URL, InsecureTLS: true})
	if !res.OK {
		t.Fatalf("expected reachable, got %+v", res)
	}
	if res.Message != "HTTP 204 No Content" {
		t.Errorf("message = %q, want HTTP 204 No Content", res.Message)
	}
}

func TestTestConnectionHTTPBasicAuth(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "opensearch" || password != "correct-password" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	ctx := privateProbeTestContext(t)
	res := testConnection(ctx, &models.Connection{
		Type: models.ConnectionTypeOpenSearch,
		URL:  ts.URL,
		Properties: map[string]string{
			"authType": "basic",
			"username": "opensearch",
			"password": "correct-password",
		},
	})
	if !res.OK {
		t.Fatalf("expected authenticated probe to succeed, got %+v", res)
	}
	if res.Message != "HTTP 204 No Content" {
		t.Errorf("message = %q, want HTTP 204 No Content", res.Message)
	}
}

func TestTestConnectionHTTPAuthenticationFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	ctx := privateProbeTestContext(t)
	res := testConnection(ctx, &models.Connection{
		Type: models.ConnectionTypeOpenSearch,
		URL:  ts.URL,
		Properties: map[string]string{
			"authType": "basic",
			"username": "opensearch",
			"password": "wrong-password",
		},
	})
	if res.OK {
		t.Fatalf("expected authentication failure, got %+v", res)
	}
	if res.Message != "HTTP 401 Unauthorized: authentication failed" {
		t.Errorf("message = %q", res.Message)
	}
}

func TestMergeStoredDraftSecrets(t *testing.T) {
	gdb := connectionInfoTestDB(t)
	existing := models.Connection{
		ID:          uuid.New(),
		Name:        "search",
		Type:        models.ConnectionTypeOpenSearch,
		URL:         "https://search.example.com",
		Username:    "admin",
		Password:    "stored-password",
		Certificate: "stored-certificate",
	}
	if err := gdb.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}

	handler := connectionActionsHandler{ctx: dbcontext.NewContext(context.Background()).WithDB(gdb, nil)}
	draft := &models.Connection{Password: "replacement-password"}
	if err := handler.mergeStoredDraftSecrets(existing.ID.String(), draft); err != nil {
		t.Fatal(err)
	}
	if draft.Password != "replacement-password" {
		t.Errorf("explicit draft password was replaced: %q", draft.Password)
	}
	if draft.Certificate != "stored-certificate" {
		t.Errorf("certificate = %q, want stored certificate", draft.Certificate)
	}
}

func TestTestConnectionRejectsPrivateAddressByDefault(t *testing.T) {
	ctx := dbcontext.NewContext(context.Background())
	ctx.ClearCache()
	res := testConnection(ctx, &models.Connection{Type: "http", URL: "http://127.0.0.1:8080"})
	if res.OK || !strings.Contains(res.Message, "prohibited address") {
		t.Fatalf("expected private address rejection, got %+v", res)
	}
}

func TestTestConnectionUnreachable(t *testing.T) {
	ctx := dbcontext.NewContext(context.Background())
	// Port 1 is reserved and not listening — the TCP connect must fail.
	res := testConnection(ctx, &models.Connection{Type: "postgres", URL: "postgres://127.0.0.1:1"})
	if res.OK {
		t.Errorf("expected unreachable for closed port, got %+v", res)
	}
}

func TestTestConnectionNoURL(t *testing.T) {
	ctx := dbcontext.NewContext(context.Background())
	if res := testConnection(ctx, &models.Connection{Type: "http"}); res.OK {
		t.Errorf("expected failure for empty url, got %+v", res)
	}
}
