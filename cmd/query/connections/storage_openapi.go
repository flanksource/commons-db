package connections

import "github.com/flanksource/clicky/rpc"

func addStorageOpenAPI(spec *rpc.OpenAPISpec) {
	spec.Paths["/api/v1/connections/storage"] = rpc.OpenAPIPath{"get": {
		Tags: []string{"Connections"}, Summary: "Connection storage destinations and KEK status", OperationID: "getConnectionStorage",
		Responses: map[string]rpc.OpenAPIResponse{"200": {
			Description: "Available storage layers and encryption key availability; never includes key material",
			Content: map[string]rpc.OpenAPIMediaType{"application/json": {Schema: &rpc.OpenAPISchema{
				Type: "object", Properties: map[string]*rpc.OpenAPISchema{
					"defaultLayer": {Type: "string"},
					"layers":       {Type: "array", Items: &rpc.OpenAPISchema{Type: "object", Properties: map[string]*rpc.OpenAPISchema{"name": {Type: "string"}, "label": {Type: "string"}, "path": {Type: "string"}, "writable": {Type: "boolean"}}}},
					"kek":          {Type: "object", Properties: map[string]*rpc.OpenAPISchema{"state": {Type: "string", Enum: []any{"ready", "missing", "unavailable", "not_configured"}}, "detail": {Type: "string"}}},
				},
			}}},
		}},
	}}
}
