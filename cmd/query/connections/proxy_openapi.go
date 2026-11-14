package connections

import "github.com/flanksource/clicky/rpc"

const connectionProxyOpenAPIPath = "/api/v1/connections/{name}/proxy/{path}"

func addProxyOpenAPI(spec *rpc.OpenAPISpec) {
	operation := func(method, operationID string) rpc.OpenAPIOperation {
		return rpc.OpenAPIOperation{
			Tags: []string{"Connections"}, Summary: method + " through an http connection", OperationID: operationID,
			Description: "Read-only reverse proxy to the connection URL joined with path and the request query. " +
				"The connection's own credentials are injected; the caller's Cookie and Authorization headers and the upstream's Set-Cookie are dropped.",
			Parameters: []rpc.OpenAPIParameter{
				{Name: "name", In: "path", Required: true, Schema: &rpc.OpenAPISchema{Type: "string"}},
				{Name: "path", In: "path", Required: true, Description: "Upstream path, may contain slashes", Schema: &rpc.OpenAPISchema{Type: "string"}},
			},
			Responses: map[string]rpc.OpenAPIResponse{
				"200": {Description: "The upstream response, passed through"},
				"400": {Description: "Not an http connection, or an invalid path"},
				"404": {Description: "Unknown connection, or one the connection policy does not allow"},
				"405": {Description: "Only GET and HEAD are proxied"},
				"502": {Description: "The upstream could not be reached"},
			},
		}
	}
	spec.Paths[connectionProxyOpenAPIPath] = rpc.OpenAPIPath{
		"get": operation("GET", "proxyConnectionGet"), "head": operation("HEAD", "proxyConnectionHead"),
	}
}
