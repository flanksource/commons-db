package profiles

import (
	"strings"
	"testing"
)

func TestParseParamValuesKeepsPlainValues(t *testing.T) {
	params, err := parseParamValues([]string{"region=eu", "filter=a=b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if params["region"] != "eu" {
		t.Fatalf("got %#v, want %q", params["region"], "eu")
	}
	if params["filter"] != "a=b" {
		t.Fatalf("only the first = separates: got %#v", params["filter"])
	}
}

// Actions are reachable over HTTP, where clicky fills the same flag map from a
// request body. Expanding @file here would read a server-side file on behalf of
// any caller, so the reference must be refused rather than honoured.
func TestParseParamValuesRefusesAFileReference(t *testing.T) {
	_, err := parseParamValues([]string{"ids=@/etc/passwd"})
	if err == nil {
		t.Fatal("expected @file to be refused on an HTTP-reachable action")
	}
	if !strings.Contains(err.Error(), "command line only") {
		t.Fatalf("error should say where @file is allowed, got %v", err)
	}
}

func TestParseParamValuesRejectsAMalformedPair(t *testing.T) {
	if _, err := parseParamValues([]string{"region"}); err == nil {
		t.Fatal("expected an error for a pair with no =")
	}
}
