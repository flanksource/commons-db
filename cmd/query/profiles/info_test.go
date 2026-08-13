package profiles

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

func decodeInfo(t *testing.T, body []byte) executionInfo {
	t.Helper()
	var info executionInfo
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatalf("decode info: %v; body=%s", err, body)
	}
	return info
}

func TestExecHandlerServesTheQueryBehindAProfileURL(t *testing.T) {
	h, next, _ := newExecTest(t, execProfile("activities"))

	rec := get(h, "/api/v1/profile/activities?region=EU&__info=true", "")
	if next.hit {
		t.Fatal("expected the info request to be served, not delegated")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != InfoContentType {
		t.Fatalf("content type = %q, want %q", contentType, InfoContentType)
	}

	info := decodeInfo(t, rec.Body.Bytes())
	if info.Profile != "activities" || info.Provider != "exec-mock" {
		t.Fatalf("info identifies %q/%q, want activities/exec-mock", info.Profile, info.Provider)
	}
	if info.URL != "/api/v1/profile/activities?region=EU" {
		t.Fatalf("url = %q, want the URL without the info marker", info.URL)
	}
	if info.Params["region"] != "EU" {
		t.Fatalf("params = %v, want region=EU and no __info", info.Params)
	}
	if info.Rows != 2 {
		t.Fatalf("rows = %d, want the 2 rows the provider returned", info.Rows)
	}
	if info.Mode != "buffered" {
		t.Fatalf("mode = %q, want buffered for a provider that cannot page", info.Mode)
	}
	if info.Diagnostics == nil || info.Diagnostics.Request.Query != "select * where region = 'EU'" {
		t.Fatalf("diagnostics do not carry the rendered query: %+v", info.Diagnostics)
	}
	if info.Headers["X-Total-Count"] != "2" {
		t.Fatalf("headers = %v, want the total the GET would report", info.Headers)
	}
}

func TestExecHandlerServesInfoByContentNegotiation(t *testing.T) {
	h, _, _ := newExecTest(t, execProfile("activities"))

	rec := get(h, "/api/v1/profile/activities?region=US", InfoContentType)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if query := decodeInfo(t, rec.Body.Bytes()).Diagnostics.Request.Query; query != "select * where region = 'US'" {
		t.Fatalf("rendered query = %q, want the US page's query", query)
	}
}

// failMock is a provider whose only behaviour is the failure an operator has to
// diagnose, which is the case "show query" exists for.
type failMock struct{}

func (failMock) Type() string { return "info-fail-mock" }
func (failMock) Execute(dbcontext.Context, query.ProviderRequest) ([]query.Row, error) {
	return nil, errors.New("relation \"activities\" does not exist")
}

func TestExecHandlerReportsTheQueryBehindAFailedInfoRequest(t *testing.T) {
	query.RegisterProvider(failMock{})
	profile := execProfile("broken")
	profile.Provider.Type = "info-fail-mock"
	h, _, _ := newExecTest(t, profile)

	rec := get(h, "/api/v1/profile/broken?region=EU&__info=true", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	var failure execError
	if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil {
		t.Fatalf("decode error: %v; body=%s", err, rec.Body.String())
	}
	if failure.Diagnostics == nil || failure.Diagnostics.Request.Query != "select * where region = 'EU'" {
		t.Fatalf("failed info request lost the query: %+v", failure.Diagnostics)
	}
	if failure.Diagnostics.Error == "" {
		t.Fatalf("failed info request lost the provider error: %+v", failure.Diagnostics)
	}
}
