package query

import (
	"strings"
	"testing"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
)

type sampleTestProvider struct{ rows []Row }

func (p sampleTestProvider) Type() string { return "postgres" }
func (p sampleTestProvider) Execute(_ dbcontext.Context, _ ProviderRequest) ([]Row, error) {
	return p.rows, nil
}

func TestSampleRendersCapsAndInfersRawRows(t *testing.T) {
	original := providerRegistry["postgres"]
	providerRegistry["postgres"] = sampleTestProvider{rows: []Row{
		{"active": true, "count": 1, "duration": time.Second, "nested": map[string]any{"x": 1}, "occurred": "2026-07-13T12:00:00.123Z", "started": time.Unix(0, 0)},
		{"active": false, "count": 2.5, "duration": 2 * time.Second, "nested": []any{1}, "occurred": "2026-07-13T12:01:00Z", "started": time.Unix(1, 0)},
	}}
	t.Cleanup(func() { providerRegistry["postgres"] = original })

	result, err := Sample(dbcontext.New(), Profile{
		Name: "sample", Provider: ProviderConfig{Type: "postgres"},
		Query:   "SELECT * FROM jobs WHERE owner = '{{.params.owner}}'",
		Params:  []ParamDef{{Name: "owner", Required: true}},
		Columns: []ColumnDef{{Name: "ignored", CEL: "row.nope"}},
	}, map[string]any{"owner": "alice"}, 1)
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if result.RenderedQuery != "SELECT * FROM jobs WHERE owner = 'alice'" {
		t.Fatalf("rendered query = %q", result.RenderedQuery)
	}
	if !result.Truncated || len(result.Rows) != 1 {
		t.Fatalf("expected one truncated row, got %#v", result)
	}
	want := []ColumnDef{
		{Name: "active", Type: ColumnTypeBoolean}, {Name: "count", Type: ColumnTypeNumber},
		{Name: "duration", Type: ColumnTypeDuration}, {Name: "nested", Type: ColumnTypeKeyValue},
		{Name: "occurred", Type: ColumnTypeDateTime},
		{Name: "started", Type: ColumnTypeDateTime},
	}
	if len(result.Columns) != len(want) {
		t.Fatalf("columns = %#v", result.Columns)
	}
	for i := range want {
		if result.Columns[i] != want[i] {
			t.Fatalf("column %d = %#v, want %#v", i, result.Columns[i], want[i])
		}
	}
}

func TestInferSampleColumnsStructuredTypes(t *testing.T) {
	columns := InferSampleColumns([]Row{
		{
			"labels": map[string]any{"env": "prod", "retries": 3},
			"pairs":  []any{map[string]any{"key": "team", "value": "core"}},
			"config": map[string]any{"nested": map[string]any{"enabled": true}},
		},
	})
	want := []ColumnDef{
		{Name: "config", Type: ColumnTypeJSON},
		{Name: "labels", Type: ColumnTypeKeyValue},
		{Name: "pairs", Type: ColumnTypeKeyValues},
	}
	if len(columns) != len(want) {
		t.Fatalf("columns = %#v", columns)
	}
	for i := range want {
		if columns[i] != want[i] {
			t.Fatalf("column %d = %#v, want %#v", i, columns[i], want[i])
		}
	}
}

func TestSampleRejectsUnsafeRequests(t *testing.T) {
	tests := []struct {
		provider, query string
		options         map[string]any
	}{
		{"postgres", "DELETE FROM jobs", nil},
		{"postgres", "SELECT 1; SELECT 2", nil},
		{"postgres", "WITH removed AS (DELETE FROM jobs RETURNING *) SELECT * FROM removed", nil},
		{"postgres", "PRAGMA journal_mode = WAL", nil},
		{"mysql", "SELECT 1 /*!; DELETE FROM jobs */", nil},
		{"http", "/jobs", map[string]any{"method": "POST"}},
		{"custom", "anything", nil},
	}
	for _, tt := range tests {
		t.Run(tt.provider+"-"+strings.Fields(tt.query)[0], func(t *testing.T) {
			_, err := Sample(dbcontext.New(), Profile{Name: "unsafe", Query: tt.query, Provider: ProviderConfig{Type: tt.provider, Options: tt.options}}, nil, 100)
			if err == nil || !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("expected read-only rejection, got %v", err)
			}
		})
	}
}

func TestReadOnlySQLIgnoresQuotedKeywordsAndTrailingSemicolon(t *testing.T) {
	for _, statement := range []string{
		"SELECT 'DELETE; DROP' AS message;",
		"/* UPDATE jobs */ WITH rows AS (SELECT 1) SELECT * FROM rows",
		"EXPLAIN SELECT * FROM [delete]",
	} {
		if err := ValidateReadOnlySQL(statement); err != nil {
			t.Errorf("%q: %v", statement, err)
		}
	}
}

func FuzzValidateReadOnlySQL(f *testing.F) {
	for _, seed := range []string{
		"SELECT 1",
		"WITH rows AS (SELECT 1) SELECT * FROM rows",
		"SELECT 'DELETE; DROP' AS message;",
		"DELETE FROM jobs",
		"SELECT 1; DROP TABLE jobs",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, statement string) {
		if err := ValidateReadOnlySQL(statement); err != nil {
			return
		}
		tokens, statements, pragmaAssignment, executableComment := scanSQL(statement)
		if statements != 1 || len(tokens) == 0 {
			t.Fatalf("accepted invalid statement framing: %q", statement)
		}
		for _, token := range tokens {
			if _, forbidden := forbiddenSQLTokens[token]; forbidden {
				t.Fatalf("accepted forbidden token %q in %q", token, statement)
			}
		}
		if tokens[0] == "pragma" && pragmaAssignment {
			t.Fatalf("accepted mutating PRAGMA: %q", statement)
		}
		if executableComment {
			t.Fatalf("accepted executable comment: %q", statement)
		}
	})
}
