package profiles

import (
	"testing"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
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

func TestLegacyTraceProviderFailsLoudly(t *testing.T) {
	_, err := legacyTraceProfileProvider{}.Execute(dbcontext.New(), query.ProviderRequest{
		Options: map[string]any{"kind": "sql"},
	})
	if err == nil || err.Error() != `legacy trace profile kind "sql" is catalog-compatible but is not executable by the query engine` {
		t.Fatalf("unexpected legacy provider error: %v", err)
	}
}
