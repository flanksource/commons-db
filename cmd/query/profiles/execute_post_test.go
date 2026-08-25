package profiles

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flanksource/commons-db/query"
)

func post(handler http.Handler, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func listParamProfile(name string) query.Profile {
	return query.Profile{
		Name:     name,
		Provider: query.ProviderConfig{Type: "exec-mock"},
		Query:    "select 1",
		Params:   []query.ParamDef{{Name: "ids", Type: query.ParamTypeList}},
	}
}

// A selection too large for a query string travels as a JSON body instead.
func TestExecHandlerRunsAListSuppliedAsAJSONArray(t *testing.T) {
	handler, next, mock := newExecTest(t, listParamProfile("bulk"))

	response := post(handler, "/api/v1/profile/bulk", `{"params":{"ids":["A-1","A-2","A-3"]}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	if next.hit {
		t.Fatal("POST on a profile should not fall through to the next handler")
	}
	ids, ok := mock.last.Params["ids"].([]string)
	if !ok {
		t.Fatalf("ids reached the provider as %T", mock.last.Params["ids"])
	}
	if len(ids) != 3 || ids[0] != "A-1" || ids[2] != "A-3" {
		t.Fatalf("ids = %#v", ids)
	}
}

func TestExecHandlerAcceptsALargeSelectionOverPOST(t *testing.T) {
	handler, _, mock := newExecTest(t, listParamProfile("bulk"))

	values := make([]string, 5000)
	quoted := make([]string, 5000)
	for i := range values {
		values[i] = "account-" + strings.Repeat("0", 4) + string(rune('a'+i%26))
		quoted[i] = `"` + values[i] + `"`
	}
	body := `{"params":{"ids":[` + strings.Join(quoted, ",") + `]}}`

	if response := post(handler, "/api/v1/profile/bulk", body); response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	// Values repeat by design, and a selection is a set: what matters is that a
	// body far past any URL limit round-trips at all.
	if got := mock.last.Params["ids"].([]string); len(got) != 26 {
		t.Fatalf("deduped selection = %d values, want 26", len(got))
	}
}

func TestExecHandlerBodyParamOverridesTheQueryString(t *testing.T) {
	handler, _, mock := newExecTest(t, listParamProfile("bulk"))

	response := post(handler, "/api/v1/profile/bulk?ids=from-url", `{"params":{"ids":["from-body"]}}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body)
	}
	if got := mock.last.Params["ids"].([]string); len(got) != 1 || got[0] != "from-body" {
		t.Fatalf("ids = %#v, want the body to win", got)
	}
}

// Ad-hoc sampling is a different POST on a sibling path; intercepting every POST
// under /profile/ would swallow it.
func TestExecHandlerLeavesProfileSampleToTheNextHandler(t *testing.T) {
	handler, next, _ := newExecTest(t, listParamProfile("bulk"))

	post(handler, "/api/v1/profile/sample", `{"profile":{"profile":"x"}}`)
	if !next.hit {
		t.Fatal("POST /profile/sample must reach the sample handler")
	}
}

func TestExecHandlerLeavesSessionStartsToTheNextHandler(t *testing.T) {
	handler, next, _ := newExecTest(t, listParamProfile("bulk"))

	post(handler, "/api/v1/profile/bulk/sessions", `{}`)
	if !next.hit {
		t.Fatal("POST /profile/{name}/sessions must reach the sessions handler")
	}
}

func TestExecHandlerRejectsANonStringValueInAList(t *testing.T) {
	handler, _, _ := newExecTest(t, listParamProfile("bulk"))

	response := post(handler, "/api/v1/profile/bulk", `{"params":{"ids":[{"nested":true}]}}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body)
	}
}

func TestExecHandlerRejectsAnUnknownBodyField(t *testing.T) {
	handler, _, _ := newExecTest(t, listParamProfile("bulk"))

	response := post(handler, "/api/v1/profile/bulk", `{"parameters":{"ids":["A-1"]}}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body)
	}
}

func TestExecHandlerAnswersPreflightForPOST(t *testing.T) {
	handler, _, _ := newExecTest(t, listParamProfile("bulk"))

	request := httptest.NewRequest(http.MethodOptions, "/api/v1/profile/bulk", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if allow := response.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(allow, "POST") {
		t.Fatalf("preflight did not advertise POST: %q (status %d)", allow, response.Code)
	}
}
