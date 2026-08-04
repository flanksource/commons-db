package paramfile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/commons-db/cmd/query/internal/paramfile"
)

// write drops content into a temp file with the given name and returns its path.
func write(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func equal(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestLoadPassesThroughAPlainValue(t *testing.T) {
	got, err := paramfile.Load("us-east,!eu")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	equal(t, got, []string{"us-east,!eu"})
}

func TestLoadCSVReadsTheFirstColumnByDefault(t *testing.T) {
	path := write(t, "accounts.csv", "account_id,region\nA-1,us-east\nA-2,eu\n")

	got, err := paramfile.Load("@" + path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	equal(t, got, []string{"A-1", "A-2"})
}

func TestLoadCSVSelectsANamedColumnCaseInsensitively(t *testing.T) {
	path := write(t, "accounts.csv", "account_id,region\nA-1,us-east\nA-2,eu\n")

	got, err := paramfile.Load("@" + path + "#Region")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	equal(t, got, []string{"us-east", "eu"})
}

func TestLoadCSVKeepsQuotedFieldsContainingCommas(t *testing.T) {
	path := write(t, "accounts.csv", "name\n\"Acme, Inc\"\nBeta\n")

	got, err := paramfile.Load("@" + path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	equal(t, got, []string{"Acme, Inc", "Beta"})
}

func TestLoadCSVNamesTheAvailableColumnsWhenTheSelectorIsWrong(t *testing.T) {
	path := write(t, "accounts.csv", "account_id,region\nA-1,us-east\n")

	_, err := paramfile.Load("@" + path + "#missing")
	if err == nil {
		t.Fatal("expected an error naming the available columns")
	}
	for _, want := range []string{"missing", "account_id", "region"} {
		if !contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestLoadJSONReadsAFlatStringArray(t *testing.T) {
	path := write(t, "ids.json", `["A-1","A-2"]`)

	got, err := paramfile.Load("@" + path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	equal(t, got, []string{"A-1", "A-2"})
}

func TestLoadJSONReadsAKeyFromAnObjectArray(t *testing.T) {
	path := write(t, "ids.json", `[{"id":"A-1","region":"eu"},{"id":"A-2","region":"us"}]`)

	got, err := paramfile.Load("@" + path + "#id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	equal(t, got, []string{"A-1", "A-2"})
}

func TestLoadJSONRequiresASelectorForObjectItems(t *testing.T) {
	path := write(t, "ids.json", `[{"id":"A-1"}]`)

	_, err := paramfile.Load("@" + path)
	if err == nil || !contains(err.Error(), "#key") {
		t.Fatalf("expected an error pointing at #key, got %v", err)
	}
}

func TestLoadJSONRejectsATopLevelObject(t *testing.T) {
	path := write(t, "ids.json", `{"id":"A-1"}`)

	_, err := paramfile.Load("@" + path)
	if err == nil || !contains(err.Error(), "an object") {
		t.Fatalf("expected an error naming what was found, got %v", err)
	}
}

func TestLoadTxtReadsOneValuePerLine(t *testing.T) {
	path := write(t, "ids.txt", "A-1\r\nA-2\n\nA-3\n")

	got, err := paramfile.Load("@" + path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	equal(t, got, []string{"A-1", "A-2", "A-3"})
}

func TestLoadTrimsDedupesAndPreservesFirstSeenOrder(t *testing.T) {
	path := write(t, "ids.txt", "  B \nA\nB\n  A  \nC\n")

	got, err := paramfile.Load("@" + path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	equal(t, got, []string{"B", "A", "C"})
}

func TestLoadPreservesAnExclusionPrefix(t *testing.T) {
	path := write(t, "ids.txt", "A-1\n!A-2\n")

	got, err := paramfile.Load("@" + path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	equal(t, got, []string{"A-1", "!A-2"})
}

func TestLoadRejectsAnUnsupportedExtension(t *testing.T) {
	path := write(t, "ids.yaml", "- A-1\n")

	_, err := paramfile.Load("@" + path)
	if err == nil || !contains(err.Error(), ".csv, .json or .txt") {
		t.Fatalf("expected an error listing the supported extensions, got %v", err)
	}
}

func TestLoadRejectsAFileWithNoValues(t *testing.T) {
	path := write(t, "ids.txt", "\n  \n")

	_, err := paramfile.Load("@" + path)
	if err == nil || !contains(err.Error(), "no values") {
		t.Fatalf("expected an empty-file error, got %v", err)
	}
}

func TestLoadReportsAMissingFile(t *testing.T) {
	_, err := paramfile.Load("@" + filepath.Join(t.TempDir(), "absent.csv"))
	if err == nil || !contains(err.Error(), "read param file") {
		t.Fatalf("expected a read error, got %v", err)
	}
}

func TestParseKeepsScalarsAsStringsAndExpandsFiles(t *testing.T) {
	path := write(t, "ids.txt", "A-1\nA-2\n")

	got, err := paramfile.Parse([]string{"region=eu", "ids=@" + path})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["region"] != "eu" {
		t.Fatalf("scalar param changed shape: %#v", got["region"])
	}
	values, ok := got["ids"].([]string)
	if !ok {
		t.Fatalf("expanded param is %T, want []string", got["ids"])
	}
	equal(t, values, []string{"A-1", "A-2"})
}

func TestParseRejectsAPairWithoutAnEquals(t *testing.T) {
	if _, err := paramfile.Parse([]string{"region"}); err == nil {
		t.Fatal("expected an error for a malformed pair")
	}
}

func TestParseKeepsAValueContainingEquals(t *testing.T) {
	got, err := paramfile.Parse([]string{"filter=a=b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["filter"] != "a=b" {
		t.Fatalf("got %#v, want %q", got["filter"], "a=b")
	}
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
