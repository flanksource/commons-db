package connections

import (
	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons-db/query/tracequery"
)

func addBrowserTracePaths(spec *rpc.OpenAPISpec) {
	if spec.Paths == nil {
		spec.Paths = map[string]rpc.OpenAPIPath{}
	}
	for suffix, response := range map[string]*rpc.OpenAPISchema{"": rpc.SchemaForStruct(tracequery.SearchResult{}), "/{traceId}": rpc.SchemaForStruct(tracequery.Detail{})} {
		params := []rpc.OpenAPIParameter{{Name: "id", In: "path", Required: true, Schema: &rpc.OpenAPISchema{Type: "string"}}}
		for name, schema := range rpc.SchemaForStruct(tracequery.Request{}).Properties {
			if name == "source" || name == "run" {
				continue
			}
			if schema.Type == "object" {
				schema = &rpc.OpenAPISchema{Type: "string", Description: "JSON object mapping values to include or exclude"}
			}
			params = append(params, rpc.OpenAPIParameter{Name: name, In: "query", Schema: schema})
		}
		if suffix != "" {
			params = append(params, rpc.OpenAPIParameter{Name: "traceId", In: "path", Required: true, Schema: &rpc.OpenAPISchema{Type: "string"}})
		}
		spec.Paths["/api/v1/connection/{id}/browser/traces"+suffix] = rpc.OpenAPIPath{"get": {Summary: "Search or inspect OpenSearch traces", Tags: []string{"Connections"}, Parameters: params, Responses: map[string]rpc.OpenAPIResponse{"200": {Description: "Trace result", Content: map[string]rpc.OpenAPIMediaType{"application/json": {Schema: response}}}, "400": {Description: "Invalid trace query or unavailable source"}, "404": {Description: "Connection or trace not found"}}}}
	}
}
