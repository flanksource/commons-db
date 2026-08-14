package profiles

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	dbcontext "github.com/flanksource/commons-db/context"
)

func newExpressionTest() (*sampleExpressionHandler, *recordingHandler) {
	next := &recordingHandler{}
	return newSampleExpressionHandler("/api/v1", dbcontext.New(), next), next
}

func evalExpression(t *testing.T, body string) sampleExpressionResponse {
	t.Helper()
	handler, _ := newExpressionTest()
	response := post(handler, "/api/v1/profile/sample/expression", body)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body)
	}
	var decoded sampleExpressionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response: %v (body = %s)", err, response.Body)
	}
	return decoded
}

const expressionRows = `[{"level":"ERROR","message":"Timeout after 5006ms"},` +
	`{"level":"ERROR","message":"\tat com.acme.pay.Gateway.charge(Gateway.java:88)"}]`

func TestSampleExpressionLeavesOtherPathsToTheNextHandler(t *testing.T) {
	handler, next := newExpressionTest()

	post(handler, "/api/v1/profile/sample", `{"profile":{"profile":"x"}}`)
	if !next.hit {
		t.Fatal("POST /profile/sample must not be claimed by the expression handler")
	}
}

func TestSampleExpressionEvaluatesEveryRowSeparately(t *testing.T) {
	decoded := evalExpression(t, `{"cel":"row.level","scope":"row","rows":`+expressionRows+`}`)

	if decoded.Error != "" {
		t.Fatalf("unexpected error: %s", decoded.Error)
	}
	if len(decoded.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(decoded.Results))
	}
	if decoded.Results[0].Value != "ERROR" {
		t.Fatalf("first value = %v, want ERROR", decoded.Results[0].Value)
	}
}

// The reason this endpoint exists: /profile/sample aborts on the first row a
// column expression cannot evaluate, so the rows that did work never come back.
func TestSampleExpressionReportsAReadNothingRowAsNull(t *testing.T) {
	decoded := evalExpression(t,
		`{"cel":"int(row.message.split(\"after \")[1].split(\"ms\")[0])","scope":"row","rows":`+expressionRows+`}`)

	if decoded.Results[0].Value == nil {
		t.Fatal("first row should have evaluated to a number")
	}
	if decoded.Results[1].Value != nil {
		t.Fatalf("second row = %#v, want null — an out-of-range index reads as nothing, not as a value",
			decoded.Results[1].Value)
	}
	if decoded.Results[1].Type != "null" {
		t.Fatalf("second row type = %q, want null", decoded.Results[1].Type)
	}
}

func TestSampleExpressionEvaluatesTheBatchScope(t *testing.T) {
	decoded := evalExpression(t,
		`{"cel":"dyn(batch).map(line, line.message + \"\").join(\"|\")","scope":"batch","rows":`+expressionRows+`}`)

	if len(decoded.Results) != 1 {
		t.Fatalf("results = %d, want 1 for a batch", len(decoded.Results))
	}
	want := "Timeout after 5006ms|\tat com.acme.pay.Gateway.charge(Gateway.java:88)"
	if decoded.Results[0].Value != want {
		t.Fatalf("value = %#v, want %#v", decoded.Results[0].Value, want)
	}
}

func TestSampleExpressionSkipsTheFirstRowInTheBoundaryScope(t *testing.T) {
	decoded := evalExpression(t, `{"cel":"index","scope":"boundary","rows":`+expressionRows+`}`)

	if len(decoded.Results) != 1 {
		t.Fatalf("results = %d, want 1 — the first row always starts a batch", len(decoded.Results))
	}
	if decoded.Results[0].Index != 1 {
		t.Fatalf("index = %d, want 1", decoded.Results[0].Index)
	}
}

// A half-written expression is the normal state of the input this serves.
func TestSampleExpressionReturnsACompileFailureAsAnAnswer(t *testing.T) {
	decoded := evalExpression(t, `{"cel":"row.level ==","scope":"row","rows":`+expressionRows+`}`)

	if decoded.Error != "" {
		t.Fatalf("a per-row failure belongs on the row, not the request: %s", decoded.Error)
	}
	if decoded.Results[0].Error == "" {
		t.Fatal("expected the parser's own message on the row")
	}
}

func TestSampleExpressionAsksForAnExpressionRatherThanFailing(t *testing.T) {
	decoded := evalExpression(t, `{"cel":"  ","scope":"row","rows":`+expressionRows+`}`)

	if decoded.Error == "" {
		t.Fatal("an empty expression should be reported, not evaluated")
	}
}

func TestSampleExpressionRejectsAnUnknownScope(t *testing.T) {
	handler, _ := newExpressionTest()
	response := post(handler, "/api/v1/profile/sample/expression",
		`{"cel":"1","scope":"column","rows":`+expressionRows+`}`)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — an unknown scope is a caller bug, not a draft", response.Code)
	}
}

func TestSampleExpressionRequiresPost(t *testing.T) {
	handler, _ := newExpressionTest()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/profile/sample/expression", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", response.Code)
	}
}
