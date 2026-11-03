package connections

import "github.com/flanksource/clicky/rpc"

func addKubernetesCatalogOpenAPI(spec *rpc.OpenAPISpec) {
	for _, route := range []string{"contexts", "namespaces", "secrets", "secrets/preview", "workloads"} {
		parameters := []rpc.OpenAPIParameter{{Name: "kubeconfig", In: "query", Description: "Optional kubeconfig file on the server", Schema: &rpc.OpenAPISchema{Type: "string"}}}
		if route != "contexts" {
			parameters = append(parameters, rpc.OpenAPIParameter{Name: "kubecontext", In: "query", Description: "Optional context in the selected kubeconfig", Schema: &rpc.OpenAPISchema{Type: "string"}}, rpc.OpenAPIParameter{Name: "namespace", In: "query", Schema: &rpc.OpenAPISchema{Type: "string"}})
		}
		if route == "secrets" || route == "secrets/preview" {
			parameters = append(parameters, rpc.OpenAPIParameter{Name: "kind", In: "query", Schema: &rpc.OpenAPISchema{Type: "string"}})
		}
		if route == "secrets/preview" {
			parameters = append(parameters, rpc.OpenAPIParameter{Name: "name", In: "query", Required: true, Schema: &rpc.OpenAPISchema{Type: "string"}})
		}
		if route == "workloads" {
			parameters = append(parameters, rpc.OpenAPIParameter{Name: "kinds", In: "query", Schema: &rpc.OpenAPISchema{Type: "string"}})
		}
		response := &rpc.OpenAPISchema{Type: "array", Items: &rpc.OpenAPISchema{Type: "object"}}
		if route == "namespaces" {
			response.Items = &rpc.OpenAPISchema{Type: "string"}
		}
		if route == "workloads" {
			response = &rpc.OpenAPISchema{Type: "object"}
		}
		if route == "contexts" {
			response = &rpc.OpenAPISchema{Type: "object", Properties: map[string]*rpc.OpenAPISchema{"currentContext": {Type: "string"}, "contexts": {Type: "array", Items: &rpc.OpenAPISchema{Type: "string"}}}}
		}
		spec.Paths["/api/v1/connections/"+route] = rpc.OpenAPIPath{"get": {Tags: []string{"Connections"}, Summary: "Read selected Kubernetes configuration metadata", Parameters: parameters, Responses: map[string]rpc.OpenAPIResponse{"200": {Description: "Discovery metadata", Content: map[string]rpc.OpenAPIMediaType{"application/json": {Schema: response}}}, "400": {Description: "Invalid Kubernetes selection"}}}}
	}
}
