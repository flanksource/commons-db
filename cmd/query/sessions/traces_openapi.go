// The trace plugin routes in the OpenAPI document: the kinds list, and one
// start operation per kind whose request body is that kind's params form.

package sessions

import (
	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons/logger"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/tracing"
)

const tracesPath = "/api/v1/traces"

// AddTracesOpenAPI describes the trace routes of the runtime current returns,
// as it is when the document is built; a server serving no trace kinds yet
// (current returns nil) describes none.
func AddTracesOpenAPI(current func() *tracing.Runtime) profiles.OpenAPIExtension {
	return func(spec *rpc.OpenAPISpec) {
		runtime := current()
		if runtime == nil {
			return
		}
		kinds, err := runtime.Kinds.List()
		if err != nil {
			logger.Warnf("trace kinds openapi: %v", err)
			return
		}
		if spec.Paths == nil {
			spec.Paths = map[string]rpc.OpenAPIPath{}
		}
		spec.Paths[tracesPath+"/kinds"] = rpc.OpenAPIPath{"get": {
			OperationID: "list-trace-kinds", Summary: "List trace kinds",
			Description: "The trace plugin kinds this server serves: each kind's params form, columns and capabilities",
			Responses:   map[string]rpc.OpenAPIResponse{"200": {Description: "KindInfo[]"}},
		}}
		for _, kind := range kinds {
			spec.Paths[tracesPath+"/"+kind.Name+"/sessions"] = rpc.OpenAPIPath{"post": traceStartOperation(kind)}
			spec.Paths[tracesPath+"/"+kind.Name+"/export"] = rpc.OpenAPIPath{"get": traceExportOperation(kind)}
		}
	}
}

func traceExportOperation(kind tracing.KindInfo) rpc.OpenAPIOperation {
	formats := make([]any, 0, len(kind.ExportFormats))
	for _, format := range kind.ExportFormats {
		formats = append(formats, format.Name)
	}
	seq := func(name, description string) rpc.OpenAPIParameter {
		return rpc.OpenAPIParameter{Name: name, In: "query", Description: description, Schema: &rpc.OpenAPISchema{Type: "integer"}}
	}
	return rpc.OpenAPIOperation{
		OperationID: "export-" + kind.Name + "-trace",
		Summary:     "Export a " + kind.Title + " trace",
		Description: "Download the records of a session's events.stream after afterSeq through toSeq as an attachment",
		Parameters: []rpc.OpenAPIParameter{
			{Name: "stream", In: "query", Required: true, Description: "The session's events.stream", Schema: &rpc.OpenAPISchema{Type: "string"}},
			{Name: "format", In: "query", Required: true, Description: "The format to download in", Schema: &rpc.OpenAPISchema{Type: "string", Enum: formats}},
			seq("afterSeq", "Export the records after this seq; 0 starts at the first"),
			seq("toSeq", "Export the records through this seq; 0 runs to the last"),
		},
		Responses: map[string]rpc.OpenAPIResponse{
			"200": {Description: "The window in the format asked"},
			"400": {Description: "No stream, a format the kind lacks, or a window that is not one"},
			"404": {Description: "No such trace kind or stream"},
		},
	}
}

func traceStartOperation(kind tracing.KindInfo) rpc.OpenAPIOperation {
	return rpc.OpenAPIOperation{
		OperationID: "start-" + kind.Name + "-trace",
		Summary:     "Start a " + kind.Title + " trace",
		Description: "Start a capture as a session; stop it via POST /api/v1/sessions/{id}/stop. Its records are read " +
			"through the profile " + tracing.ProfilePrefix + kind.Name + " with stream set to the session's events.stream.",
		RequestBody: &rpc.OpenAPIRequestBody{Required: true, Content: map[string]rpc.OpenAPIMediaType{
			"application/json": {Schema: &rpc.OpenAPISchema{Type: "object", Properties: map[string]*rpc.OpenAPISchema{
				"params":   kind.Params,
				"duration": {Type: "string", Description: "Go duration the capture runs for, e.g. 5m; empty runs for the server's longest"},
				"labels": {Type: "object", Description: "Labels the session is listed by, e.g. label.target=cycle",
					AdditionalProperties: &rpc.OpenAPISchema{Type: "string"}},
			}}},
		}},
		Responses: map[string]rpc.OpenAPIResponse{
			"201": {Description: "The started session (SessionInfo)"},
			"400": {Description: "Params the kind refuses"},
			"404": {Description: "No such trace kind"},
			"409": {Description: "Too many sessions"},
		},
	}
}
