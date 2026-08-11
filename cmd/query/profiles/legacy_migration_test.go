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

// Legacy trace profiles carry noise filters that are the whole reason some of
// them are readable, so conversion has to bring them across rather than quietly
// widening the result.
func TestLegacyFiltersSurviveConversion(t *testing.T) {
	source := `
name: oipa app logs
kubernetes:
  since: 24h
aliases:
  logger: 'map_string(_fields, "log.logger", "logger")'
filters:
  - name: drop-access-log-2xx
    description: Drop successful HTTP access log lines
    exclude: true
    hidden: true
    fields:
      logger: 'logger == "AccessLog"'
      status_2xx: 'string(message).contains("=> 2")'
  - name: errors-and-warnings
    fields:
      level: 'level == "ERROR"'
`
	profile, err := convertLegacyTraceSource(source)
	if err != nil {
		t.Fatalf("convertLegacyTraceSource() error = %v", err)
	}
	if len(profile.Filters) != 2 {
		t.Fatalf("got %d filters, want 2", len(profile.Filters))
	}

	dropped := profile.Filters[0]
	if dropped.Name != "drop-access-log-2xx" {
		t.Errorf("filter name = %q, want %q", dropped.Name, "drop-access-log-2xx")
	}
	if dropped.Description != "Drop successful HTTP access log lines" {
		t.Errorf("filter description = %q", dropped.Description)
	}
	if !dropped.Exclude || !dropped.Hidden {
		t.Errorf("exclude/hidden = %v/%v, want true/true", dropped.Exclude, dropped.Hidden)
	}
	want := map[string]string{
		"logger":     `logger == "AccessLog"`,
		"status_2xx": `string(message).contains("=> 2")`,
	}
	if !reflect.DeepEqual(dropped.Fields, want) {
		t.Errorf("fields = %#v, want %#v", dropped.Fields, want)
	}

	if quick := profile.Filters[1]; quick.Hidden || quick.Exclude {
		t.Errorf("quick filter should stay togglable and inclusive, got hidden=%v exclude=%v",
			quick.Hidden, quick.Exclude)
	}
}

func TestLegacyKubernetesTargetBecomesK8sProvider(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		wantKind string
		wantName string
	}{
		{name: "deployment", source: "target: deployment/oipa", wantKind: "Deployment", wantName: "oipa"},
		{name: "pod shorthand", source: "target: po/cycle-0", wantKind: "Pod", wantName: "cycle-0"},
		{name: "statefulset", source: "target: sts/cycle", wantKind: "StatefulSet", wantName: "cycle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, err := convertLegacyTraceSource("name: logs\nkubernetes:\n  " + tt.source + "\n")
			if err != nil {
				t.Fatalf("convertLegacyTraceSource() error = %v", err)
			}
			if profile.Provider.Type != "k8s" {
				t.Fatalf("provider = %q, want k8s", profile.Provider.Type)
			}
			if got := profile.Provider.Options["kind"]; got != tt.wantKind {
				t.Errorf("kind = %v, want %v", got, tt.wantKind)
			}
			if got := profile.Provider.Options["name"]; got != tt.wantName {
				t.Errorf("name = %v, want %v", got, tt.wantName)
			}
			// The legacy reader took the namespace from the active kubecontext;
			// the provider needs it stated, so it becomes a parameter.
			if got := profile.Provider.Options["namespace"]; got != "{{.params.namespace}}" {
				t.Errorf("namespace = %v, want the namespace parameter", got)
			}
		})
	}
}

func TestLegacyKubernetesSinceAndTail(t *testing.T) {
	profile, err := convertLegacyTraceSource("name: base\nkubernetes:\n  since: 24h\n  tail: 5000\n")
	if err != nil {
		t.Fatalf("convertLegacyTraceSource() error = %v", err)
	}
	if got := profile.Provider.Options["start"]; got != "now-24h" {
		t.Errorf("start = %v, want now-24h", got)
	}
	if got := profile.Provider.Options["limit"]; got != "5000" {
		t.Errorf("limit = %v, want \"5000\"", got)
	}
	// A base profile is imported, never run, so having no target is not an error.
	if _, ok := profile.Provider.Options["name"]; ok {
		t.Errorf("a target-less base profile should not set name")
	}
}

func TestLegacyKubernetesUnsupportedSettingsFailLoudly(t *testing.T) {
	for _, unsupported := range []string{"selector: app=oipa", "container: oipa", "grep: ERROR", "previous: true"} {
		if _, err := convertLegacyTraceSource("name: logs\nkubernetes:\n  target: po/x\n  " + unsupported + "\n"); err == nil {
			t.Errorf("converting kubernetes.%s should fail rather than silently widen the read", unsupported)
		}
	}
}

// A legacy column carries a per-cell style expression and a detail flag; both
// have direct homes on the converted column.
func TestLegacyColumnStyleAndDetail(t *testing.T) {
	profile, err := convertLegacyTraceSource(`
name: styled
index: traces-*
columns:
  - name: Level
    field: level
    style: 'level == "ERROR" ? "text-red-500" : ""'
  - name: StackTrace
    field: exception
    detail: true
`)
	if err != nil {
		t.Fatalf("convertLegacyTraceSource() error = %v", err)
	}
	want := []query.ColumnDef{
		{Name: "Level", CEL: "level", Style: `level == "ERROR" ? "text-red-500" : ""`},
		{Name: "StackTrace", CEL: "exception", Hidden: true},
	}
	if !reflect.DeepEqual(profile.Columns, want) {
		t.Fatalf("columns = %#v, want %#v", profile.Columns, want)
	}
}

// Aliases appear in the wild both as a bare expression and as a mapping with a
// cel key; the shorthand is the common one.
func TestLegacyScalarAliasShorthand(t *testing.T) {
	profile, err := convertLegacyTraceSource(`
name: aliases
index: traces-*
aliases:
  level: 'map_string(_fields, "log.level", "level")'
  logger:
    cel: 'map_string(_fields, "log.logger")'
`)
	if err != nil {
		t.Fatalf("convertLegacyTraceSource() error = %v", err)
	}
	want := []query.AliasDef{
		{Name: "level", CEL: `map_string(_fields, "log.level", "level")`},
		{Name: "logger", CEL: `map_string(_fields, "log.logger")`},
	}
	if !reflect.DeepEqual(profile.Aliases, want) {
		t.Fatalf("aliases = %#v, want %#v", profile.Aliases, want)
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
