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
		if operation != "connections" && operation != "cache" {
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
	for _, action := range Actions {
		spec.Paths[strings.TrimRight(prefix, "/")+"/"+action] = rpc.OpenAPIPath{"post": {
			Tags: []string{"Git"}, Summary: actionSummaries[action], OperationID: "git-" + action, Description: actionDescriptions[action],
			RequestBody: &rpc.OpenAPIRequestBody{Required: true, Content: map[string]rpc.OpenAPIMediaType{"application/json": {Schema: rpc.SchemaForStruct(browse.CacheAction{})}}},
			Responses: map[string]rpc.OpenAPIResponse{"200": {Description: "Git cache action result", Content: map[string]rpc.OpenAPIMediaType{"application/json": {Schema: actionSchema(action)}}},
				"400": {Description: "Invalid action or cache entry"}, "403": {Description: "Access denied"}, "404": {Description: "Unknown connection"}, "409": {Description: "A worktree of the entry is leased"}, "502": {Description: "Git provider failure"}},
		}}
	}
}

var actionSummaries = map[string]string{
	"cache-prune":  "Prune a Git cache mirror",
	"cache-evict":  "Evict a Git cache mirror",
	"cache-rebind": "Rebind a repository to another Git cache mirror",
}

var actionDescriptions = map[string]string{
	"cache-prune":  "Expires the owner mirror's commit pins unused for the configured pin TTL and garbage-collects it. Body: owner (cache key).",
	"cache-evict":  "Deletes the owner mirror with its pins and worktrees, and the bindings of repositories reading it; each clones again on its next read. Body: owner (cache key).",
	"cache-rebind": "Moves a repository reading another repository's mirror onto the named owner mirror, or the mirror sharing the most history when owner is empty. Body: connection, repo (for provider connections), owner.",
}

func actionSchema(action string) *rpc.OpenAPISchema {
	switch action {
	case "cache-prune":
		return rpc.SchemaForStruct(git.CacheEntry{})
	case "cache-evict":
		return rpc.SchemaForStruct(git.EvictResult{})
	case "cache-rebind":
		return rpc.SchemaForStruct(git.RebindResult{})
	}
	panic("unknown Git cache action " + action)
}

func operationParameters(operation string) []string {
	switch operation {
	case "refs", "cache-owners":
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
	case "cache":
		return rpc.SchemaForStruct(browse.CacheInventory{})
	case "cache-owners":
		return &rpc.OpenAPISchema{Type: "array", Items: rpc.SchemaForStruct(git.SharedOwner{})}
	}
	panic("unknown Git operation " + operation)
}
