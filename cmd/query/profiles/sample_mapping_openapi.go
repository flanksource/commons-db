package profiles

import "github.com/flanksource/clicky/rpc"

func addMappingPreviewOpenAPI(spec *rpc.OpenAPISpec) {
	row := &rpc.OpenAPISchema{Type: "object", AdditionalProperties: &rpc.OpenAPISchema{}}
	rows := &rpc.OpenAPISchema{Type: "array", Items: row}
	spec.Paths["/api/v1/profile/sample/query"] = rpc.OpenAPIPath{"post": {
		Summary: "Translate canonical and native span queries", OperationID: "translate-span-query",
		Description: "Resolves the selected connection/index layout and translates query field names and values in either direction without executing a search.",
		RequestBody: &rpc.OpenAPIRequestBody{Required: true, Content: map[string]rpc.OpenAPIMediaType{"application/json": {Schema: &rpc.OpenAPISchema{Type: "object", Required: []string{"profile", "direction"}, Properties: map[string]*rpc.OpenAPISchema{"profile": row, "direction": {Type: "string", Enum: []any{"source", "canonical"}}, "query": row, "params": row}}}}},
		Responses:   map[string]rpc.OpenAPIResponse{"200": {Description: "Logical search, canonical DSL, native DSL and resolved layout", Content: map[string]rpc.OpenAPIMediaType{"application/json": {Schema: row}}}, "400": {Description: "Invalid request"}, "422": {Description: "Missing, ambiguous or non-invertible mapping"}},
	}}
	spec.Paths["/api/v1/profile/sample/mapping"] = rpc.OpenAPIPath{"post": {
		Summary: "Preview canonical span and listing mappings", OperationID: "preview-profile-mapping",
		Description: "Applies normalization, aliases and columns to retained sample input without querying a provider. Maximum 1000 rows and 2 MiB request body.",
		RequestBody: &rpc.OpenAPIRequestBody{Required: true, Content: map[string]rpc.OpenAPIMediaType{"application/json": {Schema: &rpc.OpenAPISchema{Type: "object", Required: []string{"profile", "rows"}, Properties: map[string]*rpc.OpenAPISchema{"profile": row, "rows": rows}}}}},
		Responses: map[string]rpc.OpenAPIResponse{
			"200": {Description: "Normalized rows, available canonical columns, mapped rows and formatted table", Content: map[string]rpc.OpenAPIMediaType{"application/json": {Schema: &rpc.OpenAPISchema{Type: "object", Properties: map[string]*rpc.OpenAPISchema{"normalizedRows": rows, "availableColumns": rows, "rows": rows, "resultColumns": rows, "document": row}}}}},
			"400": {Description: "Invalid request"}, "422": {Description: "Invalid span or column mapping"},
		},
	}}
}
