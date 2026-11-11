package providers

import (
	"encoding/json"
	"fmt"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/otel/span/layout"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/esdsl"
)

type SpanQueryTranslation struct {
	Profile   query.Profile    `json:"profile"`
	Direction layout.Direction `json:"direction"`
	Query     json.RawMessage  `json:"query,omitempty"`
	Params    map[string]any   `json:"params,omitempty"`
}
type TranslatedSpanQuery struct {
	Search    esdsl.Search      `json:"search"`
	Canonical json.RawMessage   `json:"canonical"`
	Native    json.RawMessage   `json:"native"`
	Layout    layout.Definition `json:"layout"`
}

// TranslateSpanQuery inspects only mappings; it never executes a data search.
func TranslateSpanQuery(ctx context.Context, input SpanQueryTranslation) (*TranslatedSpanQuery, error) {
	req := query.ProviderRequest{Connection: input.Profile.Provider.Connection, Options: input.Profile.Provider.Options, Search: &esdsl.Search{}, Spans: input.Profile.Spans, Params: input.Params, ParamRoles: map[string]query.ParamRole{}}
	for _, param := range input.Profile.Params {
		req.ParamRoles[param.Name] = param.Role
	}
	var resolved *layout.Definition
	switch input.Profile.Provider.Type {
	case "opensearch":
		runtime, err := openSearchClient(ctx, req)
		if err != nil {
			return nil, err
		}
		resolved = runtime.options.Layout
	case "opentelemetry":
		runtime, err := openTelemetrySearchClient(ctx, req)
		if err != nil {
			return nil, err
		}
		resolved = runtime.options.Layout
	default:
		return nil, fmt.Errorf("canonical translation requires OpenSearch or OpenTelemetry")
	}
	result := &TranslatedSpanQuery{Layout: *resolved}
	switch input.Direction {
	case layout.ToCanonical:
		if len(input.Query) == 0 && input.Profile.Provider.Options["search"] != nil {
			encoded, err := json.Marshal(input.Profile.Provider.Options["search"])
			if err != nil {
				return nil, err
			}
			var native esdsl.Search
			if err := json.Unmarshal(encoded, &native); err != nil {
				return nil, err
			}
			logical, err := resolved.TranslateSearch(native, layout.ToCanonical)
			if err != nil {
				return nil, err
			}
			result.Search = logical
			compiled, err := esdsl.Compile(esdsl.CompileRequest{Search: logical, Params: openSearchParamBindings(req), TimeFieldMapping: &esdsl.TimeFieldMapping{Type: "date"}})
			if err != nil {
				return nil, err
			}
			if logical.Size != nil {
				compiled.Body["size"] = compiled.Size
			}
			result.Canonical, err = json.Marshal(compiled.Body)
			if err != nil {
				return nil, err
			}
			result.Native, err = resolved.Translate(result.Canonical, layout.ToSource)
			return result, err
		}
		result.Native = input.Query
		if len(result.Native) == 0 {
			result.Native = json.RawMessage(`{}`)
		}
		canonical, err := resolved.Translate(result.Native, layout.ToCanonical)
		if err != nil {
			return nil, err
		}
		result.Canonical = canonical
	case layout.ToSource:
		canonical := input.Query
		if len(canonical) == 0 {
			if input.Profile.Search == nil {
				return nil, fmt.Errorf("canonical search is required")
			}
			compiled, err := esdsl.Compile(esdsl.CompileRequest{Search: *input.Profile.Search, Params: openSearchParamBindings(req), TimeFieldMapping: &esdsl.TimeFieldMapping{Type: "date"}})
			if err != nil {
				return nil, err
			}
			if input.Profile.Search.Size != nil {
				compiled.Body["size"] = compiled.Size
			}
			canonical, err = json.Marshal(compiled.Body)
			if err != nil {
				return nil, err
			}
		}
		result.Canonical = canonical
		native, err := resolved.Translate(canonical, layout.ToSource)
		if err != nil {
			return nil, err
		}
		result.Native = native
	default:
		return nil, fmt.Errorf("direction must be source or canonical")
	}
	parsed, err := esdsl.Parse(result.Canonical)
	if err != nil {
		return nil, err
	}
	result.Search = parsed
	if input.Direction == layout.ToSource && len(input.Query) == 0 && input.Profile.Search != nil {
		result.Search = *input.Profile.Search
	}
	return result, nil
}
