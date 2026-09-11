package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	captools "github.com/flanksource/captain/pkg/ai/tools"
	capchat "github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky/entity"
	"github.com/spf13/cobra"
)

func TestIsQueryChatTool(t *testing.T) {
	tests := []struct {
		name string
		tool captools.ToolInfo
		want bool
	}{
		{name: "connection list", tool: captools.ToolInfo{Operation: &entity.RPCOperation{Name: "connection"}}, want: true},
		{name: "dynamic profile", tool: captools.ToolInfo{Name: "profile-orders"}, want: true},
		{name: "serve", tool: captools.ToolInfo{Operation: &entity.RPCOperation{Name: "serve"}}, want: false},
		{name: "schema fallback name", tool: captools.ToolInfo{Name: "schema"}, want: false},
		{name: "version", tool: captools.ToolInfo{Operation: &entity.RPCOperation{Name: "version"}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isQueryChatTool(tt.tool); got != tt.want {
				t.Fatalf("isQueryChatTool(%+v) = %v, want %v", tt.tool, got, tt.want)
			}
		})
	}
}

func TestQueryToolStrategies(t *testing.T) {
	tests := []struct {
		name    string
		tool    captools.ToolInfo
		want    api.ToolPolicy
		matched bool
	}{
		{name: "get method", tool: captools.ToolInfo{Operation: &entity.RPCOperation{Method: http.MethodGet}}, want: api.ToolPolicyAllow, matched: true},
		{name: "head method", tool: captools.ToolInfo{Operation: &entity.RPCOperation{Method: http.MethodHead}}, want: api.ToolPolicyAllow, matched: true},
		{name: "list verb overrides post", tool: captools.ToolInfo{Operation: &entity.RPCOperation{Method: http.MethodPost, Clicky: &entity.ClickyOperationMeta{Verb: "list"}}}, want: api.ToolPolicyAllow, matched: true},
		{name: "post method", tool: captools.ToolInfo{Operation: &entity.RPCOperation{Method: http.MethodPost}}, want: api.ToolPolicyAsk, matched: true},
		{name: "unknown remains unresolved", tool: captools.ToolInfo{}, want: api.ToolPolicyAuto, matched: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, matched := api.ResolveStrategies(queryToolStrategies(), tt.tool)
			if got != tt.want || matched != tt.matched {
				t.Fatalf("query tool policy = (%v, %v), want (%v, %v)", got, matched, tt.want, tt.matched)
			}
		})
	}
}

func TestQueryRuntimeProfile(t *testing.T) {
	profile, err := queryRuntimeProfile(context.Background())
	if err != nil {
		t.Fatalf("queryRuntimeProfile: %v", err)
	}
	got := []any{
		profile.System,
		profile.Composed.Spec.Model.Name,
		len(profile.Composed.Trace),
		profile.Composed.Trace[0].Name,
		profile.Composed.Trace[0].Scope,
	}
	want := []any{
		"You are a database operations assistant. Use the available tools to inspect connections, query profiles, and profile results. Prefer tools over guessing, never invent connection details, and summarize results clearly.",
		"api:sonnet-5",
		1,
		"query",
		api.SpecLayerGlobal,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("query runtime profile = %#v, want %#v", got, want)
	}
}

func TestQueryRuntimeProfileRejectsUnknownSelection(t *testing.T) {
	_, err := queryRuntimeProfile(context.Background(), capchat.WithRuntimeProfileRef("missing"))
	if err == nil {
		t.Fatal("queryRuntimeProfile accepted an unsupported profile selection")
	}
}

func TestQueryChatServerCatalogFiltersProcessCommands(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	root := &cobra.Command{Use: "query"}
	root.AddCommand(
		&cobra.Command{Use: "connection", Run: func(*cobra.Command, []string) {}},
		&cobra.Command{Use: "serve", Run: func(*cobra.Command, []string) {}},
		&cobra.Command{Use: "schema", Run: func(*cobra.Command, []string) {}},
		&cobra.Command{Use: "version", Run: func(*cobra.Command, []string) {}},
	)
	chat, err := newQueryChatServer(root)
	if err != nil {
		t.Fatalf("newQueryChatServer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/chat/tools", nil)
	res := httptest.NewRecorder()
	chat.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("GET /api/chat/tools status = %d, body = %s", res.Code, res.Body.String())
	}
	var catalog struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("decode tool catalog: %v", err)
	}
	if len(catalog.Tools) != 1 || catalog.Tools[0].Name != "connection" {
		t.Fatalf("tool catalog = %+v, want only connection", catalog.Tools)
	}
}
