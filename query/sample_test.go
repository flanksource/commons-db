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
		Aliases: []AliasDef{{Name: "active_copy", CEL: "row.active"}},
		Ignore:  []string{"started"},
		Columns: []ColumnDef{{Name: "computed", CEL: "row.count + 1"}},
	}, SampleOptions{
		Params: map[string]any{"owner": "alice"},
		Page:   PageRequest{Limit: 1},
	})
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if result.RenderedQuery != "SELECT * FROM jobs WHERE owner = 'alice'" {
		t.Fatalf("rendered query = %q", result.RenderedQuery)
	}
	if result.Truncated || !result.Pagination.HasMore || len(result.Rows) != 1 {
		t.Fatalf("expected one pageable row with more available, got %#v", result)
	}
	want := []ColumnDef{
		{Name: "active", Type: ColumnTypeBoolean}, {Name: "active_copy", Type: ColumnTypeBoolean}, {Name: "computed", Type: ColumnTypeNumber}, {Name: "count", Type: ColumnTypeNumber},
		{Name: "duration", Type: ColumnTypeDuration}, {Name: "nested", Type: ColumnTypeKeyValue},
		{Name: "occurred", Type: ColumnTypeDateTime},
	}
	if len(result.Columns) != len(want) {
		t.Fatalf("columns = %#v", result.Columns)
	}
	for i := range want {
		if result.Columns[i] != want[i] {
			t.Fatalf("column %d = %#v, want %#v", i, result.Columns[i], want[i])
		}
	}
	if result.Rows[0]["active_copy"] != true {
		t.Fatalf("sample alias transform not applied: %#v", result.Rows[0])
	}
	if got := result.Rows[0]["computed"]; got != int64(2) {
		t.Fatalf("sample computed transform not applied: value=%v type=%T row=%#v", got, got, result.Rows[0])
	}
	if _, ok := result.Rows[0]["started"]; ok {
		t.Fatalf("ignored sample field survived: %#v", result.Rows[0])
	}
}

func TestSampleRejectsPageAboveProfileMaximum(t *testing.T) {
	original := providerRegistry["postgres"]
	providerRegistry["postgres"] = sampleTestProvider{rows: []Row{{"value": 1}}}
	t.Cleanup(func() { providerRegistry["postgres"] = original })

	_, err := Sample(dbcontext.New(), Profile{
		Name: "bounded", Provider: ProviderConfig{Type: "postgres"}, Query: "SELECT 1",
		Limits: &RowLimits{PageSize: 25, MaxPageSize: 25},
	}, SampleOptions{Page: PageRequest{Limit: 26}})
	if err == nil || !strings.Contains(err.Error(), "maximum page size 25") {
		t.Fatalf("error = %v, want maximum page size", err)
	}
}

func TestSampleAppliesRowTransformsOnce(t *testing.T) {
	original := providerRegistry["postgres"]
	providerRegistry["postgres"] = sampleTestProvider{rows: []Row{{"value": 1}}}
	t.Cleanup(func() { providerRegistry["postgres"] = original })

	result, err := Sample(dbcontext.New(), Profile{
		Name:     "single-transform",
		Provider: ProviderConfig{Type: "postgres"},
		Query:    "SELECT 1 AS value",
		Columns:  []ColumnDef{{Name: "value", CEL: "row.value + 1"}},
	}, SampleOptions{Page: PageRequest{Limit: 1}})
	if err != nil {
		t.Fatalf("Sample: %v", err)
	}
	if got := result.Rows[0]["value"]; got != int64(2) {
		t.Fatalf("transformed value = %v (%T), want 2", got, got)
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

// An OpenSearch mapping has no type for an identifier — a UUID field is
// `keyword` like every other string — so the shape of the values is the only
// signal both providers share.
func TestInferSampleColumnsIdentifiers(t *testing.T) {
	columns := InferSampleColumns([]Row{
		{
			"id":     "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			"digest": "9e107d9d372bb6826bd81d3542a419d6",
			"region": "eu-west-1",
			"mixed":  "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
		},
		{
			"id":     "6ba7b811-9dad-11d1-80b4-00c04fd430c8",
			"digest": "e4d909c290d0fb1ca068ffaddf22cbd0",
			"region": "us-east-1",
			"mixed":  "not-an-identifier",
		},
	})
	want := map[string]ColumnType{
		// 32 bare hex digits is equally an MD5 digest, and a digest is a value
		// someone might well want to pick from a list.
		"digest": ColumnTypeString,
		"id":     ColumnTypeUUID,
		"mixed":  ColumnTypeString,
		"region": ColumnTypeString,
	}
	got := map[string]ColumnType{}
	for _, column := range columns {
		got[column.Name] = column.Type
	}
	if len(got) != len(want) {
		t.Fatalf("columns = %#v", columns)
	}
	for name, expected := range want {
		if got[name] != expected {
			t.Errorf("column %q type = %q, want %q", name, got[name], expected)
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
		{"http", "/jobs", map[string]any{"method": "POST"}},
		{"custom", "anything", nil},
	}
	for _, tt := range tests {
		t.Run(tt.provider+"-"+strings.Fields(tt.query)[0], func(t *testing.T) {
			_, err := Sample(dbcontext.New(), Profile{Name: "unsafe", Query: tt.query, Provider: ProviderConfig{Type: tt.provider, Options: tt.options}}, SampleOptions{
				Page: PageRequest{Limit: 100},
			})
			if err == nil || !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("expected read-only rejection, got %v", err)
			}
		})
	}
}

// The read-only check must run on the rendered request: a param that reaches
// the query or the options through a template is otherwise invisible to it.
func TestSampleRejectsUnsafeTemplatedRequests(t *testing.T) {
	tests := []struct {
		name    string
		profile Profile
		params  map[string]any
	}{
		{
			name: "http-method",
			profile: Profile{
				Name:     "templated-method",
				Query:    "/jobs",
				Provider: ProviderConfig{Type: "http", Options: map[string]any{"method": "{{.params.m}}"}},
				Params:   []ParamDef{{Name: "m"}},
			},
			params: map[string]any{"m": "POST"},
		},
		{
			name: "sql-statement",
			profile: Profile{
				Name:     "templated-sql",
				Query:    "SELECT * FROM jobs; {{.params.tail}}",
				Provider: ProviderConfig{Type: "postgres"},
				Params:   []ParamDef{{Name: "tail"}},
			},
			params: map[string]any{"tail": "DELETE FROM jobs"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Sample(dbcontext.New(), tt.profile, SampleOptions{
				Params: tt.params,
				Page:   PageRequest{Limit: 100},
			})
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
