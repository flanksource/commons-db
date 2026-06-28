package schema

import (
	"encoding/json"
	"fmt"

	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons-db/types"
)

// baseConnectionForm is the minimal field set every connection shares: name and
// namespace plus the generic properties map. The discriminator `type` is added
// separately (its enum is dynamic). The per-type fields (url, credentials,
// certificate, ...) live on the provider structs (connection_providers.go) so a
// connection only shows the fields it uses. The order= tags drive the render
// order (emitted as x-clicky-order, sorted by clicky-ui).
type baseConnectionForm struct {
	Name       string              `json:"name"       clicky:"title=Name,order=0,required"`
	Namespace  string              `json:"namespace"  clicky:"type=k8s-namespace-selector,title=Namespace,order=1"`
	Properties types.JSONStringMap `json:"properties" clicky:"title=Properties,order=7"`
}

// Connection returns the JSON Schema for the connection form. A minimal base
// (name/namespace/type/properties) is always present; an `allOf` of if/then
// branches keyed on `type` adds the per-type fields for the backends modelled in
// connection_providers.go, so the form adapts to the selected type. The
// `x-discriminator` hint tells clicky-ui to render the `type` picker (an icon
// grid, via x-enum-icons) first, then the matched type's form.
func Connection() Schema {
	flat := reflectStruct(baseConnectionForm{})
	props := Schema{}
	for name, raw := range flat["properties"].(map[string]any) {
		props[name] = Schema(raw.(map[string]any))
	}
	props["type"] = Schema{
		"type":           "string",
		"title":          "Type",
		"enum":           connectionTypeEnum(),
		"x-enum-icons":   connectionTypeIcons,
		"x-enum-display": "grid",
	}

	var allOf []any
	for _, typ := range allConnectionTypes {
		if cfg, ok := tailoredProviders[typ]; ok {
			allOf = append(allOf, tailoredBranch(typ, cfg))
		}
	}

	return Schema{
		"$schema":         Draft,
		"title":           "Connection",
		"type":            "object",
		"required":        []string{"name", "type"},
		"properties":      props,
		"x-discriminator": "type",
		"allOf":           allOf,
	}
}

// tailoredBranch builds one `{if: type==X, then: {...}}` conditional by reflecting
// a provider struct: top-level fields land in `then.properties`, `property=`-tagged
// fields nest under the `properties` object (the connection's Properties map).
func tailoredBranch(typ string, cfg any) Schema {
	flat := reflectStruct(cfg)

	props := Schema{}
	propProps := Schema{}
	for name, raw := range flat["properties"].(map[string]any) {
		fs := Schema(raw.(map[string]any))
		if key, ok := fs["x-clicky-property"].(string); ok {
			delete(fs, "x-clicky-property")
			propProps[key] = fs
			continue
		}
		props[name] = fs
	}

	if len(propProps) > 0 {
		props["properties"] = Schema{"type": "object", "title": "Properties", "properties": propProps}
	}

	then := Schema{}
	if len(props) > 0 {
		then["properties"] = props
	}
	if req := stringSlice(flat["required"]); len(req) > 0 {
		then["required"] = req
	}

	return Schema{
		"if": Schema{
			"properties": Schema{"type": Schema{"const": typ}},
			"required":   []string{"type"},
		},
		"then": then,
	}
}

// reflectStruct reflects a provider struct into a Schema map via clicky, honoring
// the clicky:"..." widget tags and the EnvVar SchemaDescriber.
func reflectStruct(cfg any) Schema {
	b, err := json.Marshal(rpc.SchemaForStruct(cfg))
	if err != nil {
		panic(fmt.Sprintf("reflect connection provider %T: %v", cfg, err))
	}
	var m Schema
	if err := json.Unmarshal(b, &m); err != nil {
		panic(fmt.Sprintf("decode connection provider %T: %v", cfg, err))
	}
	return m
}

// stringSlice converts a reflected JSON []any of strings to []string.
func stringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, len(raw))
	for i, e := range raw {
		out[i] = e.(string)
	}
	return out
}
