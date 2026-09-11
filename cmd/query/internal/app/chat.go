package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	captools "github.com/flanksource/captain/pkg/ai/tools"
	capchat "github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	clickyaichat "github.com/flanksource/clicky/aichat"
	"github.com/spf13/cobra"
)

const queryChatSystemPrompt = "You are a database operations assistant. Use the available tools " +
	"to inspect connections, query profiles, and profile results. Prefer tools " +
	"over guessing, never invent connection details, and summarize results clearly."

// newQueryChatServer exposes the same Clicky/Cobra operations that back the
// explorer as in-process AI tools. Provider initialization remains lazy: the
// server can start without an API key and reports the configuration error only
// when chat is used.
func newQueryChatServer(root *cobra.Command) (*capchat.Service, error) {
	provider, err := clickyaichat.NewCobraToolProvider(clickyaichat.CobraToolProviderOptions{
		Root: root, Filter: isQueryChatTool, Strategies: queryToolStrategies(),
	})
	if err != nil {
		return nil, err
	}
	return capchat.NewService(capchat.ServiceOptions{
		Profile:        capchat.RuntimeProfileProviderFunc(queryRuntimeProfile),
		ToolStrategies: provider.Strategies(),
		ToolPolicy:     provider.ToolPolicy(),
		Tools:          provider,
		Threads:        capchat.FixedThreadStore(capchat.NewMemoryThreadStore()),
	}), nil
}

func queryRuntimeProfile(_ context.Context, options ...capchat.RuntimeProfileOption) (capchat.RuntimeProfile, error) {
	if selection := capchat.ApplyRuntimeProfileOptions(options...); selection.Ref != "" {
		return capchat.RuntimeProfile{}, capchat.RequestError(
			http.StatusBadRequest,
			fmt.Sprintf("runtime profile %q cannot be selected: query serves only its default profile", selection.Ref),
		)
	}
	composed, err := api.ComposeSpecLayers(api.ResolveSpecOptions{Layers: []api.SpecLayer{{
		Name: "query", Scope: api.SpecLayerGlobal,
		Spec: api.Spec{Model: api.Model{Name: "api:sonnet-5"}},
	}}})
	if err != nil {
		return capchat.RuntimeProfile{}, fmt.Errorf("compose query chat runtime profile: %w", err)
	}
	return capchat.RuntimeProfile{System: queryChatSystemPrompt, Composed: composed}, nil
}

// isQueryChatTool removes query's process-management and long-running
// interactive commands from the tool catalog. Starting another server,
// printing schemas or build metadata, and blocking on a live trace/top stream,
// are useful on the CLI but not meaningful for the in-app assistant (sessions are
// managed via the REST API instead).
func isQueryChatTool(tool captools.ToolInfo) bool {
	name := strings.ToLower(strings.TrimSpace(tool.OperationName()))
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(tool.Name))
	}
	switch name {
	case "serve", "schema", "trace", "top", "version":
		return false
	default:
		return true
	}
}

func queryToolStrategies() []api.PermissionStrategy {
	return []api.PermissionStrategy{api.HTTPVerbStrategy{}, api.MCPHintStrategy{}, queryReadOnlyStrategy{}}
}

type queryReadOnlyStrategy struct{}

func (queryReadOnlyStrategy) Resolve(tool api.ToolInfo) (api.ToolPolicy, bool) {
	switch strings.ToLower(strings.TrimSpace(tool.Verb())) {
	case "get", "list":
		return api.ToolPolicyAllow, true
	default:
		return api.ToolPolicyAuto, false
	}
}
