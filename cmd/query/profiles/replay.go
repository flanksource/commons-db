package profiles

import (
	"context"
	"fmt"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/commons-db/query"
)

// ReplayFlags are the flags of the profiles `replay` action. They become cobra
// flags on the CLI and the request body over HTTP, from one declaration.
type ReplayFlags struct {
	Params  []string `flag:"param" help:"Profile filter param as key=value (repeatable)"`
	Select  []string `flag:"select" help:"Pick the row to replay as column=value (repeatable); must match exactly one row"`
	Target  string   `flag:"target" help:"Send to this target: a connection reference or an absolute URL"`
	Method  string   `flag:"method" help:"Override the resolved HTTP method"`
	URL     string   `flag:"url" help:"Override the resolved request URL or path"`
	Body    string   `flag:"body" help:"Override the request body" stdin:"true"`
	Header  []string `flag:"header" help:"Set a request header as key=value (repeatable)"`
	Execute bool     `flag:"execute" help:"Actually send the request; without this the action only previews it"`
	Hash    string   `flag:"preview-hash" help:"Refuse to send unless the rebuilt request still hashes to this value"`
}

func (ReplayFlags) ClickyActionFlags() {}

// ReplayResult carries the preview always and the response only when the
// request was actually sent, so preview and execute share one response type.
type ReplayResult struct {
	Preview  *query.ReplayPreview       `json:"preview"`
	Executed *query.ReplayExecuteResult `json:"executed,omitempty"`
}

func (r ReplayResult) Pretty() api.Text {
	if r.Preview == nil {
		return api.Text{}
	}
	text := api.Text{Content: fmt.Sprintf("%s %s\n", r.Preview.Method, r.Preview.URL)}
	for name, value := range r.Preview.Headers {
		text.Children = append(text.Children, api.Text{Content: fmt.Sprintf("  %s: %s\n", name, value), Style: "text-muted"})
	}
	if r.Preview.BodyPreview != "" {
		text.Children = append(text.Children, api.Text{Content: "\n" + r.Preview.BodyPreview + "\n"})
	}
	if r.Executed == nil {
		text.Children = append(text.Children, api.Text{
			Content: "\nnot sent — pass --execute to send this request\n", Style: "text-yellow-500"})
		return text
	}
	style := "text-green-500"
	if r.Executed.StatusCode >= 400 {
		style = "text-red-500"
	}
	text.Children = append(text.Children, api.Text{
		Content: fmt.Sprintf("\n%s in %dms\n", r.Executed.Status, r.Executed.DurationMS), Style: style})
	if r.Executed.ResponsePreview != "" {
		text.Children = append(text.Children, api.Text{Content: r.Executed.ResponsePreview + "\n"})
	}
	return text
}

// Replay resolves a profile, runs it, turns the selected row back into the
// outbound HTTP request its replay block describes, and sends it only when
// asked to.
//
// Preview is the default because replay re-drives a real side effect into a real
// system. The two-step preview/execute handshake, guarded by the preview hash,
// is what stops a caller from approving one request and sending another after
// the underlying data moved.
func (s *Service) Replay(ctx context.Context, name string, options ReplayFlags) (ReplayResult, error) {
	preview, err := s.buildReplayPreview(ctx, name, options)
	if err != nil {
		return ReplayResult{}, err
	}
	if !options.Execute {
		return ReplayResult{Preview: preview}, nil
	}
	if options.Hash != "" && options.Hash != preview.Hash {
		return ReplayResult{Preview: preview}, fmt.Errorf(
			"replay preview is stale: the request now hashes to %s, not %s; preview it again before sending",
			preview.Hash, options.Hash)
	}
	executed, err := query.ExecuteReplay(s.context(), preview)
	if err != nil {
		return ReplayResult{Preview: preview}, err
	}
	return ReplayResult{Preview: preview, Executed: executed}, nil
}

func (s *Service) buildReplayPreview(ctx context.Context, name string, options ReplayFlags) (*query.ReplayPreview, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	resolved, err := Resolve(ctx, store, name)
	if err != nil {
		return nil, err
	}
	if resolved.Profile.Replay == nil {
		return nil, fmt.Errorf("profile %q declares no replay block", name)
	}

	params, err := parseParamValues(options.Params)
	if err != nil {
		return nil, err
	}
	selector, err := parseKeyValues("select", options.Select)
	if err != nil {
		return nil, err
	}
	headers, err := parseKeyValues("header", options.Header)
	if err != nil {
		return nil, err
	}

	queryCtx := s.context().Wrap(ctx)
	result, err := query.Execute(queryCtx, resolved.Profile, params)
	if err != nil {
		return nil, err
	}

	return query.BuildReplayPreview(queryCtx, query.ReplayBuildOptions{
		Profile:        resolved.Profile,
		Rows:           result.Rows,
		Select:         selector,
		TargetOverride: options.Target,
		MethodOverride: options.Method,
		URLOverride:    options.URL,
		BodyOverride:   options.Body,
		Headers:        headers,
	})
}
