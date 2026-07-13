package main

import (
	"strings"

	"github.com/flanksource/clicky/aichat"
	"github.com/spf13/cobra"
)

// newQueryChatServer exposes the same Clicky/Cobra operations that back the
// explorer as in-process AI tools. Provider initialization remains lazy: the
// server can start without an API key and reports the configuration error only
// when chat is used.
func newQueryChatServer(root *cobra.Command) *aichat.Server {
	return aichat.NewServer(aichat.Options{
		RootCmd: root,
		System: "You are a database operations assistant. Use the available tools " +
			"to inspect connections, query profiles, and profile results. Prefer tools " +
			"over guessing, never invent connection details, and summarize results clearly.",
		Threads:            aichat.NewMemThreadStore(),
		ToolFilter:         isQueryChatTool,
		ToolApprovalPolicy: queryToolRequiresApproval,
	})
}

// isQueryChatTool removes query's process-management and long-running
// interactive commands from the tool catalog. Starting another server,
// printing schemas, or blocking on a live trace/top stream is useful on the
// CLI but is not a meaningful operation for the in-app assistant (sessions are
// managed via the REST API instead).
func isQueryChatTool(tool aichat.ToolInfo) bool {
	name := strings.ToLower(strings.TrimSpace(tool.OperationName))
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(tool.Name))
	}
	switch name {
	case "serve", "schema", "trace", "top":
		return false
	default:
		return true
	}
}

// queryToolRequiresApproval auto-runs safe HTTP/read verbs and gates everything
// else. The UI may still make a deliberate per-tool On/Ask/Off choice for an
// individual request.
func queryToolRequiresApproval(tool aichat.ToolInfo, _ any) bool {
	switch strings.ToUpper(strings.TrimSpace(tool.Method)) {
	case "GET", "HEAD", "OPTIONS":
		return false
	}
	switch strings.ToLower(strings.TrimSpace(tool.ClickyVerb)) {
	case "get", "list":
		return false
	default:
		return true
	}
}
