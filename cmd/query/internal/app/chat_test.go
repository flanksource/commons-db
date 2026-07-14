package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flanksource/clicky/aichat"
	"github.com/spf13/cobra"
)

func TestIsQueryChatTool(t *testing.T) {
	tests := []struct {
		name string
		tool aichat.ToolInfo
		want bool
	}{
		{name: "connection list", tool: aichat.ToolInfo{OperationName: "connection"}, want: true},
		{name: "dynamic profile", tool: aichat.ToolInfo{Name: "profile-orders"}, want: true},
		{name: "serve", tool: aichat.ToolInfo{OperationName: "serve"}, want: false},
		{name: "schema fallback name", tool: aichat.ToolInfo{Name: "schema"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isQueryChatTool(tt.tool); got != tt.want {
				t.Fatalf("isQueryChatTool(%+v) = %v, want %v", tt.tool, got, tt.want)
			}
		})
	}
}

func TestQueryToolRequiresApproval(t *testing.T) {
	tests := []struct {
		name string
		tool aichat.ToolInfo
		want bool
	}{
		{name: "get method", tool: aichat.ToolInfo{Method: "GET"}, want: false},
		{name: "head method", tool: aichat.ToolInfo{Method: "head"}, want: false},
		{name: "list verb", tool: aichat.ToolInfo{ClickyVerb: "list"}, want: false},
		{name: "post method", tool: aichat.ToolInfo{Method: "POST"}, want: true},
		{name: "unknown defaults safe", tool: aichat.ToolInfo{}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := queryToolRequiresApproval(tt.tool, nil); got != tt.want {
				t.Fatalf("queryToolRequiresApproval(%+v) = %v, want %v", tt.tool, got, tt.want)
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
	chat := newQueryChatServer(root)
	t.Cleanup(func() { _ = chat.Close() })

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
