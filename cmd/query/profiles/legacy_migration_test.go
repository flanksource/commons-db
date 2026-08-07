package profiles

import (
	"reflect"
	"testing"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
)

func TestLegacyTraceKind(t *testing.T) {
	tests := []struct {
		name    string
		profile legacyTraceProfile
		want    string
	}{
		{name: "explicit", profile: legacyTraceProfile{Kind: "watch"}, want: "watch"},
		{name: "sql", profile: legacyTraceProfile{SQL: map[string]any{}}, want: "sql"},
		{name: "kubernetes", profile: legacyTraceProfile{Kubernetes: map[string]any{}}, want: "kubernetes"},
		{name: "arthas", profile: legacyTraceProfile{Arthas: map[string]any{}}, want: "arthas"},
		{name: "opensearch", profile: legacyTraceProfile{Index: "traces-*"}, want: "opensearch"},
		{name: "replay", profile: legacyTraceProfile{Replay: map[string]any{}}, want: "replay"},
		{name: "import", profile: legacyTraceProfile{Imports: []string{"base"}}, want: "import"},
		{name: "unknown", profile: legacyTraceProfile{}, want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := legacyTraceKind(tt.profile); got != tt.want {
				t.Fatalf("legacyTraceKind() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLegacyOpenTelemetrySearch(t *testing.T) {
	search, err := legacyOpenTelemetrySearch(map[string]legacyTraceParam{
		"namespace": {Field: "process.serviceName"},
		"levels":    {Field: "level", Operator: "terms"},
		"phrase":    {Field: "message", Operator: "match_phrase", Clause: "must"},
		"exclude":   {Field: "env", Operator: "wildcard", Clause: "must_not"},
		"lucene":    {Field: "body", Operator: "query_string"},
		"errored":   {Field: "error", Operator: "exists"},
		"hidden":    {Field: "tenant", Internal: true},
	})
	if err != nil {
		t.Fatalf("legacyOpenTelemetrySearch() error: %v", err)
	}
	want := &esdsl.Search{Query: &esdsl.Condition{Op: esdsl.OpBool, Conditions: []esdsl.Condition{
		{Op: esdsl.OpExists, Field: "error", When: "errored"},
		{Op: esdsl.OpWildcard, Field: "env", Occur: esdsl.OccurMustNot, Value: esdsl.Param("exclude"), Optional: true},
		{Op: esdsl.OpTerms, Field: "level", Value: esdsl.Param("levels"), Optional: true},
		{Op: esdsl.OpQueryString, Fields: []string{"body"}, Value: esdsl.Param("lucene"), Optional: true},
		{Op: esdsl.OpTerms, Field: "process.serviceName", Value: esdsl.Param("namespace"), Optional: true},
		{Op: esdsl.OpMatchPhrase, Field: "message", Occur: esdsl.OccurMust, Value: esdsl.Param("phrase"), Optional: true},
	}}}
	if !reflect.DeepEqual(search, want) {
		t.Fatalf("legacyOpenTelemetrySearch() =\n%+v\nwant\n%+v", search.Query.Conditions, want.Query.Conditions)
	}
}

func TestLegacyOpenTelemetrySearchRejections(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]legacyTraceParam
		want   string
	}{
		{
			name:   "unknown operator",
			params: map[string]legacyTraceParam{"level": {Field: "level", Operator: "span_near"}},
			want:   `param "level": unsupported operator "span_near"`,
		},
		{
			name:   "missing field",
			params: map[string]legacyTraceParam{"level": {Operator: "term"}},
			want:   `param "level": field is required`,
		},
		{
			name:   "unknown clause",
			params: map[string]legacyTraceParam{"level": {Field: "level", Clause: "maybe"}},
			want:   `query.conditions[0]: unknown occur "maybe"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := legacyOpenTelemetrySearch(tt.params)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("legacyOpenTelemetrySearch() error = %v, want %q", err, tt.want)
			}
		})
	}
}

// An internal parameter contributes no condition but keeps its declaration, so
// supplying one still fails instead of silently widening the query.
func TestLegacyInternalParamKeepsItsDeclaration(t *testing.T) {
	profile, err := convertLegacyTraceProfile(legacyTraceProfile{
		Name: "traces", Index: "traces-*",
		Params: map[string]legacyTraceParam{
			"tenant":    {Field: "tenant", Internal: true},
			"namespace": {Field: "process.serviceName"},
		},
	}, "")
	if err != nil {
		t.Fatalf("convertLegacyTraceProfile() error: %v", err)
	}
	names := make([]string, 0, len(profile.Params))
	for _, param := range profile.Params {
		names = append(names, param.Name)
	}
	if !reflect.DeepEqual(names, []string{"namespace", "tenant"}) {
		t.Fatalf("migrated params = %v, want [namespace tenant]", names)
	}
	search := profile.Provider.Options["search"].(*esdsl.Search)
	if len(search.Query.Conditions) != 1 || search.Query.Conditions[0].Field != "process.serviceName" {
		t.Fatalf("migrated search conditions = %+v, want only process.serviceName", search.Query.Conditions)
	}
}

func TestLegacyTraceProviderFailsLoudly(t *testing.T) {
	_, err := legacyTraceProfileProvider{}.Execute(dbcontext.New(), query.ProviderRequest{
		Options: map[string]any{"kind": "sql"},
	})
	if err == nil || err.Error() != `legacy trace profile kind "sql" is catalog-compatible but is not executable by the query engine` {
		t.Fatalf("unexpected legacy provider error: %v", err)
	}
}
