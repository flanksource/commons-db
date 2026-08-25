package profiles

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type recordingHandler struct{ hit bool }

func (h *recordingHandler) ServeHTTP(http.ResponseWriter, *http.Request) { h.hit = true }

const jsonPathRow = `{"metadata":{"user":{"email":"ada@example.com"}},` +
	`"payload":"{\"status\":\"OPEN\"}",` +
	`"tags":[{"value":"prod"},{"value":"core"}]}`

func newJSONPathTest() (*sampleJSONPathHandler, *recordingHandler) {
	next := &recordingHandler{}
	return newSampleJSONPathHandler("/api/v1", next), next
}

func evalJSONPath(t *testing.T, body string) sampleJSONPathResponse {
	t.Helper()
	handler, _ := newJSONPathTest()
	response := post(handler, "/api/v1/profile/sample/jsonpath", body)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body)
	}
	var decoded sampleJSONPathResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v (body = %s)", err, response.Body)
	}
	return decoded
}

func TestSampleJSONPathLeavesOtherPathsToTheNextHandler(t *testing.T) {
	handler, next := newJSONPathTest()

	post(handler, "/api/v1/profile/sample", `{"profile":{"profile":"x"}}`)
	if !next.hit {
		t.Fatal("POST /profile/sample must not be claimed by the jsonpath handler")
	}
}

func TestSampleJSONPathRequiresPOST(t *testing.T) {
	handler, next := newJSONPathTest()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/profile/sample/jsonpath", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405; body = %s", response.Code, response.Body)
	}
	if next.hit {
		t.Fatal("the jsonpath path should not fall through on a wrong method")
	}
}

func TestSampleJSONPathRejectsAnUnknownBodyField(t *testing.T) {
	handler, _ := newJSONPathTest()

	response := post(handler, "/api/v1/profile/sample/jsonpath", `{"path":"$.a","row":{}}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body)
	}
}

func TestSampleJSONPathResolvesARowRootedPath(t *testing.T) {
	got := evalJSONPath(t, `{"jsonpath":"$.metadata.user.email","row":`+jsonPathRow+`}`)

	if got.Count != 1 || got.Matches[0] != "ada@example.com" {
		t.Fatalf("matches = %#v (count %d)", got.Matches, got.Count)
	}
	if got.FilterField != "metadata.user.email" {
		t.Fatalf("filterField = %q, want the literal chain", got.FilterField)
	}
	if got.Error != "" {
		t.Fatalf("error = %q, want none", got.Error)
	}
}

// The backend decodes a column carrying JSON as text when a source names it, so
// the preview has to as well or it would disagree with the column it previews.
func TestSampleJSONPathDecodesAJSONEncodedSourceColumn(t *testing.T) {
	got := evalJSONPath(t, `{"jsonpath":"$.status","source":"payload","row":`+jsonPathRow+`}`)

	if got.Count != 1 || got.Matches[0] != "OPEN" {
		t.Fatalf("matches = %#v (count %d)", got.Matches, got.Count)
	}
	if got.FilterField != "payload.status" {
		t.Fatalf("filterField = %q, want the source-prefixed chain", got.FilterField)
	}
}

func TestSampleJSONPathReportsEveryMatchAndNoFilterFieldForAWildcard(t *testing.T) {
	got := evalJSONPath(t, `{"jsonpath":"$.tags[*].value","row":`+jsonPathRow+`}`)

	if got.Count != 2 || got.Matches[0] != "prod" || got.Matches[1] != "core" {
		t.Fatalf("matches = %#v (count %d)", got.Matches, got.Count)
	}
	// A wildcard selects rather than addresses, so the column stays unfilterable
	// unless its author declares filter.field — the playground must say so.
	if got.FilterField != "" {
		t.Fatalf("filterField = %q, want none for a wildcard", got.FilterField)
	}
}

func TestSampleJSONPathReportsNoMatchAsAnEmptyResult(t *testing.T) {
	got := evalJSONPath(t, `{"jsonpath":"$.metadata.user.phone","row":`+jsonPathRow+`}`)

	if got.Count != 0 || len(got.Matches) != 0 || got.Error != "" {
		t.Fatalf("matches = %#v (count %d, error %q)", got.Matches, got.Count, got.Error)
	}
}

// A half-written path is the normal state of the input this serves: the parse
// error is the answer, not a failed request.
func TestSampleJSONPathAnswersAnInvalidPathWithItsParseError(t *testing.T) {
	got := evalJSONPath(t, `{"jsonpath":"$.[","row":`+jsonPathRow+`}`)

	if got.Error == "" {
		t.Fatal("an unparseable path should report why")
	}
	if got.Count != 0 || len(got.Matches) != 0 {
		t.Fatalf("matches = %#v, want none alongside an error", got.Matches)
	}
}

func TestSampleJSONPathAnswersAnEmptyPathWithoutEvaluating(t *testing.T) {
	got := evalJSONPath(t, `{"jsonpath":"   ","row":`+jsonPathRow+`}`)

	if got.Error != "enter a JSONPath expression" {
		t.Fatalf("error = %q", got.Error)
	}
}
