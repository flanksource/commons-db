package httpapi

import (
	"strings"

	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/git/browse"
)

func AddOpenAPI(spec *rpc.OpenAPISpec, prefix string) {
	if spec.Paths == nil {
		spec.Paths = map[string]rpc.OpenAPIPath{}
	}
	for _, operation := range Operations {
		parameters := []rpc.OpenAPIParameter{}
		if operation != "connections" {
			parameters = append(parameters, rpc.OpenAPIParameter{Name: "connection", In: "query", Required: true, Schema: &rpc.OpenAPISchema{Type: "string"}})
		}
		for _, name := range operationParameters(operation) {
			schema := &rpc.OpenAPISchema{Type: "string"}
			if name == "limit" {
				schema.Type = "integer"
			}
			if name == "extension" {
				schema.Type, schema.Items = "array", &rpc.OpenAPISchema{Type: "string"}
			}
			parameters = append(parameters, rpc.OpenAPIParameter{Name: name, In: "query", Required: name == "ref" || name == "pipeline", Schema: schema})
		}
		spec.Paths[strings.TrimRight(prefix, "/")+"/"+operation] = rpc.OpenAPIPath{"get": {
			Tags: []string{"Git"}, Summary: "List Git " + operation, OperationID: "git-" + operation, Parameters: parameters,
			Description: "Read-only Git discovery using configured connections. Repository is required for provider connections and omitted for pinned Git connections. Requires host authorization. Pipeline discovery supports Azure DevOps and GitHub Actions.",
			Responses: map[string]rpc.OpenAPIResponse{"200": {Description: "Git discovery result", Content: map[string]rpc.OpenAPIMediaType{"application/json": {Schema: responseSchema(operation)}}},
				"400": {Description: "Invalid source or query"}, "403": {Description: "Access denied"}, "404": {Description: "Unknown connection or file"}, "501": {Description: "Provider capability unsupported"}, "502": {Description: "Git provider failure"}},
		}}
	}
}

func operationParameters(operation string) []string {
	switch operation {
	case "refs":
		return []string{"repo"}
	case "files":
		return []string{"repo", "ref", "extension"}
	case "commits":
		return []string{"repo", "ref", "cursor", "limit"}
	case "pipelines":
		return []string{"repo", "cursor", "limit"}
	case "pipeline-executions":
		return []string{"repo", "pipeline", "branch", "cursor", "limit"}
	}
	return nil
}

func responseSchema(operation string) *rpc.OpenAPISchema {
	switch operation {
	case "connections":
		return &rpc.OpenAPISchema{Type: "array", Items: rpc.SchemaForStruct(browse.Connection{})}
	case "repos":
		return &rpc.OpenAPISchema{Type: "array", Items: rpc.SchemaForStruct(browse.Repository{})}
	case "refs":
		return rpc.SchemaForStruct(browse.Refs{})
	case "files":
		return rpc.SchemaForStruct(browse.Files{})
	case "commits":
		return rpc.SchemaForStruct(git.Page[browse.Commit]{})
	case "pipelines":
		return rpc.SchemaForStruct(git.Page[git.Pipeline]{})
	case "pipeline-executions":
		return rpc.SchemaForStruct(git.Page[git.PipelineExecution]{})
	}
	panic("unknown Git operation " + operation)
}
