package schema

import (
	"encoding/json"
	"fmt"
	"slices"

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

// ConnectionSource returns the externally referenced connection schema graph.
// Bundle converts it to the self-contained document returned by Connection.
func ConnectionSource() Schema {
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
		"x-enum-display": "combobox",
	}

	var allOf []any
	for _, typ := range allConnectionTypes {
		allOf = append(allOf, Schema{
			"if": Schema{
				"properties": Schema{"type": Schema{"const": typ}},
				"required":   []string{"type"},
			},
			"then": Schema{"$ref": "connections/" + typ + ".json"},
		})
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

// ConnectionComponents returns a standalone form schema for every known
// connection type. Types without tailored fields still receive the base form.
func ConnectionComponents() map[string]Schema {
	components := make(map[string]Schema, len(allConnectionTypes))
	for _, typ := range allConnectionTypes {
		flat := reflectStruct(baseConnectionForm{})
		props := Schema{}
		for name, raw := range flat["properties"].(map[string]any) {
			props[name] = Schema(raw.(map[string]any))
		}
		props["type"] = Schema{"type": "string", "title": "Type", "const": typ}
		required := []string{"name", "type"}
		if cfg, ok := tailoredProviders[typ]; ok {
			branch := tailoredBranch(typ, cfg)["then"].(Schema)
			for name, raw := range branch["properties"].(Schema) {
				props[name] = raw
			}
			required = appendUnique(required, stringSlice(branch["required"])...)
		}
		components[typ] = Schema{
			"$schema":    Draft,
			"$id":        typ + ".json",
			"title":      "Connection: " + typ,
			"type":       "object",
			"required":   required,
			"properties": props,
		}
	}
	return components
}

// Connection returns the bundled schema consumed by clicky-ui.
func Connection() Schema {
	refs := SchemaRefs("connections", ConnectionComponents())
	bundled, err := Bundle(ConnectionSource(), refs)
	if err != nil {
		panic(fmt.Sprintf("bundle connection schema: %v", err))
	}
	return bundled
}

// SchemaRefs keys component schemas by their relative external-ref path.
func SchemaRefs(dir string, components map[string]Schema) map[string]Schema {
	refs := make(map[string]Schema, len(components))
	for name, component := range components {
		refs[dir+"/"+name+".json"] = component
	}
	return refs
}

func appendUnique(base []string, values ...string) []string {
	seen := make(map[string]bool)
	for _, value := range base {
		seen[value] = true
	}
	for _, value := range values {
		if !seen[value] {
			base = append(base, value)
			seen[value] = true
		}
	}
	return base
}

// tailoredBranch builds one `{if: type==X, then: {...}}` conditional by reflecting
// a provider struct: top-level fields land in `then.properties`, `property=`-tagged
// fields nest under the `properties` object (the connection's Properties map).
func tailoredBranch(typ string, cfg any) Schema {
	flat := reflectStruct(cfg)

	props := Schema{}
	propProps := Schema{}
	propertyRequired := []string{}
	required := stringSlice(flat["required"])
	for name, raw := range flat["properties"].(map[string]any) {
		fs := Schema(raw.(map[string]any))
		if key, ok := fs["x-clicky-property"].(string); ok {
			delete(fs, "x-clicky-property")
			if typ == "opentelemetry" && key == "connection" {
				fs["x-clicky-lookup"] = Schema{
					"url": "/api/v1/connection", "filter": "connection", "searchParam": "__lookup_q", "multi": false,
					"scope": Schema{"param": "types", "from": "type", "map": map[string][]string{"opentelemetry": {"opensearch"}}},
				}
			}
			propProps[key] = fs
			if slices.Contains(required, name) {
				propertyRequired = append(propertyRequired, key)
				required = slices.DeleteFunc(required, func(value string) bool { return value == name })
			}
			continue
		}
		props[name] = fs
	}

	if len(propProps) > 0 {
		properties := Schema{"type": "object", "title": "Properties", "properties": propProps}
		if len(propertyRequired) > 0 {
			properties["required"] = propertyRequired
			required = append(required, "properties")
		}
		props["properties"] = properties
	}
	if isHTTPConnectionType(typ) {
		props["properties"] = httpAuthenticationSchema()
	}

	then := Schema{}
	if len(props) > 0 {
		then["properties"] = props
	}
	if len(required) > 0 {
		then["required"] = required
	}

	return Schema{
		"if": Schema{
			"properties": Schema{"type": Schema{"const": typ}},
			"required":   []string{"type"},
		},
		"then": then,
	}
}

func isHTTPConnectionType(typ string) bool {
	switch typ {
	case "http", "opensearch", "prometheus", "loki", "jaeger":
		return true
	default:
		return false
	}
}

// httpAuthenticationSchema stores the selected mode and its credentials in the
// connection Properties map. Keeping the discriminator inside this object lets
// JsonSchemaForm evaluate ordinary nested allOf/if/then clauses while the Go
// model continues to persist the values in its existing JSON column.
func httpAuthenticationSchema() Schema {
	secret := func(title string, order int, source string) Schema {
		field := Schema{
			"type":               "string",
			"title":              title,
			"format":             "password",
			"x-clicky-component": "k8s-secret-selector",
			"x-clicky-order":     order,
		}
		if source != "" {
			field["x-clicky-default-source"] = source
		}
		return field
	}

	authType := Schema{
		"type":           "string",
		"title":          "Authentication",
		"enum":           []string{"none", "basic", "oauth", "mtls"},
		"default":        "none",
		"x-enum-display": "segmented",
		"x-enum-labels":  map[string]string{"none": "None", "basic": "Basic", "oauth": "OAuth", "mtls": "mTLS"},
		"x-clicky-order": 0,
	}

	condition := func(authType string, properties Schema, required ...string) Schema {
		then := Schema{"properties": properties}
		if len(required) > 0 {
			then["required"] = required
		}
		return Schema{
			"if": Schema{
				"properties": Schema{"authType": Schema{"const": authType}},
				"required":   []string{"authType"},
			},
			"then": then,
		}
	}

	return Schema{
		"type":           "object",
		"title":          "Authentication",
		"x-clicky-order": 4,
		"properties":     Schema{"authType": authType},
		"required":       []string{"authType"},
		"allOf": []any{
			condition("basic", Schema{
				"username": secret("Username", 1, "value"),
				"password": secret("Password", 2, "secret"),
			}, "username", "password"),
			condition("oauth", Schema{
				"clientID":     secret("Client ID", 1, "value"),
				"clientSecret": secret("Client Secret", 2, "secret"),
				"tokenURL":     Schema{"type": "string", "title": "Token URL", "x-clicky-order": 3},
				"scopes":       Schema{"type": "string", "title": "Scopes", "description": "Comma-separated OAuth scopes", "x-clicky-order": 4},
			}, "clientID", "clientSecret", "tokenURL"),
			condition("mtls", Schema{
				"ca":   secret("CA Certificate", 1, "secret"),
				"cert": secret("Client Certificate", 2, "secret"),
				"key":  secret("Client Private Key", 3, "secret"),
			}, "cert", "key"),
		},
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
	if values, ok := v.([]string); ok {
		return values
	}
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
