package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	captools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
	"github.com/spf13/cobra"
)

func TestIsQueryChatTool(t *testing.T) {
	tests := []struct {
		name string
		tool captools.ToolInfo
		want bool
	}{
		{name: "connection list", tool: captools.ToolInfo{Annotations: map[string]string{"clicky/operation": "connection"}}, want: true},
		{name: "dynamic profile", tool: captools.ToolInfo{Name: "profile-orders"}, want: true},
		{name: "serve", tool: captools.ToolInfo{Annotations: map[string]string{"clicky/operation": "serve"}}, want: false},
		{name: "schema fallback name", tool: captools.ToolInfo{Name: "schema"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isQueryChatTool(tt.tool); got != tt.want {
				t.Fatalf("isQueryChatTool(%+v) = %v, want %v", tt.tool, got, tt.want)
			}
		})
	}
}

func TestQueryToolPermission(t *testing.T) {
	tests := []struct {
		name string
		tool captools.ToolInfo
		want api.ToolMode
	}{
		{name: "get method", tool: captools.ToolInfo{Annotations: map[string]string{"clicky/method": "GET"}}, want: api.ToolModeOn},
		{name: "head method", tool: captools.ToolInfo{Annotations: map[string]string{"clicky/method": "head"}}, want: api.ToolModeOn},
		{name: "list verb", tool: captools.ToolInfo{Annotations: map[string]string{"clicky/verb": "list"}}, want: api.ToolModeOn},
		{name: "post method", tool: captools.ToolInfo{Annotations: map[string]string{"clicky/method": "POST"}}, want: api.ToolModeAsk},
		{name: "unknown defaults safe", tool: captools.ToolInfo{}, want: api.ToolModeAsk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := queryToolPermission(tt.tool); got != tt.want {
				t.Fatalf("queryToolPermission(%+v) = %v, want %v", tt.tool, got, tt.want)
			}
		})
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
