package app

import (
	"context"
	"strings"

	captools "github.com/flanksource/captain/pkg/ai/tools"
	capchat "github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	clickyaichat "github.com/flanksource/clicky/aichat"
	"github.com/spf13/cobra"
)

// newQueryChatServer exposes the same Clicky/Cobra operations that back the
// explorer as in-process AI tools. Provider initialization remains lazy: the
// server can start without an API key and reports the configuration error only
// when chat is used.
func newQueryChatServer(root *cobra.Command) (*capchat.Service, error) {
	provider, err := clickyaichat.NewCobraToolProvider(clickyaichat.CobraToolProviderOptions{
		Root: root, Filter: isQueryChatTool, Permission: queryToolPermission,
	})
	if err != nil {
		return nil, err
	}
	return capchat.NewService(capchat.ServiceOptions{
		Settings: capchat.RuntimeSettingsProviderFunc(func(context.Context) (capchat.RuntimeSettings, error) {
			return capchat.RuntimeSettings{
				Spec: api.Spec{Model: api.Model{Name: "api:sonnet-5"}},
				System: "You are a database operations assistant. Use the available tools " +
					"to inspect connections, query profiles, and profile results. Prefer tools " +
					"over guessing, never invent connection details, and summarize results clearly.",
			}, nil
		}),
		Tools: provider, Threads: capchat.NewMemoryThreadStore(),
	}), nil
}

// isQueryChatTool removes query's process-management and long-running
// interactive commands from the tool catalog. Starting another server,
// printing schemas, or blocking on a live trace/top stream is useful on the
// CLI but is not a meaningful operation for the in-app assistant (sessions are
// managed via the REST API instead).
func isQueryChatTool(tool captools.ToolInfo) bool {
	name := strings.ToLower(strings.TrimSpace(tool.Annotation("clicky/operation")))
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
func queryToolPermission(tool captools.ToolInfo) api.ToolMode {
	switch strings.ToUpper(strings.TrimSpace(tool.Annotation("clicky/method"))) {
	case "GET", "HEAD", "OPTIONS":
		return api.ToolModeOn
	}
	switch strings.ToLower(strings.TrimSpace(tool.Annotation("clicky/verb"))) {
	case "get", "list":
		return api.ToolModeOn
	default:
		return api.ToolModeAsk
	}
}
