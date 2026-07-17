package profiles

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons-db/query"
	"github.com/spf13/cobra"
)

type profileOpenAPIHandler struct {
	root      *cobra.Command
	config    *rpc.Config
	generator *rpc.OpenAPIGenerator
	store     Store
}

func newProfileOpenAPIHandler(root *cobra.Command, config *rpc.Config, store Store) http.Handler {
	return &profileOpenAPIHandler{
		root:   root,
		config: config,
		generator: rpc.NewOpenAPIGenerator(&rpc.OpenAPIConfig{
			Title: "Query", Description: "Connections, profiles and execution", Version: "0.1.0",
		}),
		store: store,
	}
}

func (h *profileOpenAPIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", http.MethodGet+", "+http.MethodOptions)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	spec, err := h.generator.GenerateFromCobraWithConfig(h.root, h.config)
	if err != nil {
		http.Error(w, fmt.Sprintf("generate OpenAPI: %v", err), http.StatusInternalServerError)
		return
	}
	if err := mergeStoredProfiles(spec, h.store); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(spec); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// mergeStoredProfiles replaces startup-snapshotted profile surfaces with a
// fresh view of the YAML store. The resulting operations execute through the
// generic /profile/profile-<slug> handler, so no live Cobra mutation is needed.
func mergeStoredProfiles(spec *rpc.OpenAPISpec, store Store) error {
	if spec.Clicky == nil {
		spec.Clicky = &rpc.ClickySpecMeta{}
	}
	surfaces := make([]rpc.ClickySurface, 0, len(spec.Clicky.Surfaces))
	for _, surface := range spec.Clicky.Surfaces {
		if surface.Parent != profileSurfaceParent {
			surfaces = append(surfaces, surface)
		}
	}
	spec.Clicky.Surfaces = surfaces

	for path, methods := range spec.Paths {
		for method, operation := range methods {
			if operation.Clicky != nil && operation.Clicky.Surface != "" && strings.HasPrefix(operation.Clicky.Surface, "profile-") {
				delete(methods, method)
			}
		}
		if len(methods) == 0 {
			delete(spec.Paths, path)
		}
	}

	profiles, err := store.List(context.Background())
	if err != nil {
		return fmt.Errorf("load profile surfaces: %w", err)
	}
	for _, profile := range profiles {
		resolved, err := Resolve(context.Background(), store, profile.Name)
		if err != nil {
			return fmt.Errorf("resolve profile surface %q: %w", profile.Name, err)
		}
		addProfileToSpec(spec, resolved.Profile)
	}
	return nil
}

func addProfileToSpec(spec *rpc.OpenAPISpec, profile query.Profile) {
	entityName := "profile-" + slugify(profile.Name)
	spec.Clicky.Surfaces = append(spec.Clicky.Surfaces, rpc.ClickySurface{
		Key:         entityName,
		Entity:      entityName,
		Title:       profile.Name,
		Parent:      profileSurfaceParent,
		Description: "Run " + profile.Name,
		Icon:        providerIcon(profile.Provider.Type),
	})

	parameters := make([]rpc.OpenAPIParameter, 0, len(profile.Params)+2)
	roles := map[query.ParamRole]bool{}
	for _, param := range profile.Params {
		parameters = append(parameters, profileParameter(param))
		roles[param.Role] = true
	}
	if !roles[query.ParamRoleLimit] {
		parameters = append(parameters,
			rpc.OpenAPIParameter{
				Name: "limit", In: "query", Description: "Rows per page (maximum 1000)",
				Schema: &rpc.OpenAPISchema{Type: "integer", Default: defaultPageLimit},
				Clicky: &rpc.ClickyParameterMeta{Role: "limit"},
			})
	}
	if !roles[query.ParamRoleOffset] {
		parameters = append(parameters,
			rpc.OpenAPIParameter{
				Name: "offset", In: "query", Description: "Rows to skip",
				Schema: &rpc.OpenAPISchema{Type: "integer", Default: 0},
				Clicky: &rpc.ClickyParameterMeta{Role: "offset"},
			})
	}
	path := "/api/v1/profile/" + entityName
	spec.Paths[path] = rpc.OpenAPIPath{"get": {
		Summary:     "Run " + profile.Name,
		Description: "Execute the stored query profile",
		OperationID: "run-" + entityName,
		Parameters:  parameters,
		Responses: map[string]rpc.OpenAPIResponse{
			"200": {
				Description: "Profile rows",
				Content: map[string]rpc.OpenAPIMediaType{
					"application/json": {Schema: profileResponseSchema(profile)},
				},
			},
		},
		Clicky: &rpc.ClickyOperationMeta{
			Command: entityName,
			Surface: entityName,
			Verb:    "list",
			Scope:   "collection",
			Export:  profileExportMeta(profile),
		},
	}}

	if profile.Kind() != query.KindQuery {
		spec.Paths[path+"/sessions"] = rpc.OpenAPIPath{"post": {
			Summary:     "Start a " + string(profile.Kind()) + " session for " + profile.Name,
			Description: "Start a live session; follow it via GET /api/v1/sessions/{id}/events (SSE) and stop it via DELETE /api/v1/sessions/{id}",
			OperationID: "start-" + entityName + "-session",
			Parameters:  parameters,
			Responses: map[string]rpc.OpenAPIResponse{
				"201": {Description: "Session started"},
			},
		}}
	}
}

func profileExportMeta(profile query.Profile) *rpc.ExportMeta {
	meta := &rpc.ExportMeta{
		Formats:       []string{"json", "ndjson", "csv", "yaml", "markdown", "html", "excel", "pdf"},
		Scopes:        []string{"page"},
		FormatMaxRows: map[string]int{"pdf": maxPDFRows},
	}
	if supportsAllRows(profile.Provider.Type) {
		meta.Scopes = append(meta.Scopes, "all")
		if len(profile.Processors) > 0 || profile.Top != nil {
			meta.AllRowsMode = "buffered"
		} else {
			meta.AllRowsMode = "streaming"
		}
	}
	return meta
}

func profileParameter(param query.ParamDef) rpc.OpenAPIParameter {
	schema := &rpc.OpenAPISchema{Type: "string", Title: param.DisplayLabel(), Description: param.Description}
	switch param.Type {
	case query.ParamTypeNumber:
		schema.Type = "number"
	case query.ParamTypeBoolean:
		schema.Type = "boolean"
	case query.ParamTypeDate:
		schema.Format = "date-time"
	}
	for _, option := range param.Options {
		schema.Enum = append(schema.Enum, option)
	}
	if param.Default != nil {
		schema.Default = param.Default
	}
	role := string(param.Role)
	if role == "" {
		role = string(query.ParamRoleFilter)
	}
	return rpc.OpenAPIParameter{
		Name:        param.Name,
		In:          "query",
		Description: param.Description,
		Required:    param.Required,
		Schema:      schema,
		Clicky:      &rpc.ClickyParameterMeta{Role: role},
	}
}

func profileResponseSchema(profile query.Profile) *rpc.OpenAPISchema {
	properties := map[string]*rpc.OpenAPISchema{}
	idAssigned := false
	for _, column := range profile.Columns {
		if column.Hidden {
			continue
		}
		property := columnOpenAPISchema(column.Type)
		property.Extensions = map[string]any{}
		if column.Type != "" {
			property.Extensions["x-clicky-type"] = string(column.Type)
		}
		if column.Label != "" {
			property.Extensions["x-clicky-label"] = column.Label
		}
		if column.Format != "" {
			property.Extensions["x-clicky-format"] = column.Format
		}
		if column.Kind != "" {
			property.Extensions["x-clicky-kind"] = string(column.Kind)
		}
		if !idAssigned {
			property.Extensions["x-clicky-id"] = true
			property.Extensions["x-clicky-name"] = true
			idAssigned = true
		}
		properties[column.Name] = property
	}
	if !idAssigned {
		properties["id"] = &rpc.OpenAPISchema{
			Type: "string", Extensions: map[string]any{"x-clicky-id": true, "x-clicky-name": true},
		}
	}
	item := &rpc.OpenAPISchema{Type: "object", Properties: properties}
	if render := profile.RenderMode(); render != "" {
		item.Extensions = map[string]any{"x-clicky-render": render}
	}
	return &rpc.OpenAPISchema{Type: "array", Items: item}
}

func columnOpenAPISchema(columnType query.ColumnType) *rpc.OpenAPISchema {
	switch columnType {
	case query.ColumnTypeNumber:
		return &rpc.OpenAPISchema{Type: "number"}
	case query.ColumnTypeBoolean:
		return &rpc.OpenAPISchema{Type: "boolean"}
	case query.ColumnTypeKeyValue:
		return &rpc.OpenAPISchema{Type: "object", AdditionalProperties: &rpc.OpenAPISchema{}}
	case query.ColumnTypeKeyValues:
		return &rpc.OpenAPISchema{
			Type: "array",
			Items: &rpc.OpenAPISchema{
				Type: "object",
				Properties: map[string]*rpc.OpenAPISchema{
					"key":   {Type: "string"},
					"value": {},
				},
				Required: []string{"key", "value"},
			},
		}
	case query.ColumnTypeJSON:
		return &rpc.OpenAPISchema{
			Nullable: true,
			OneOf: []*rpc.OpenAPISchema{
				{Type: "object"},
				{Type: "array"},
				{Type: "string"},
				{Type: "number"},
				{Type: "boolean"},
			},
		}
	default:
		return &rpc.OpenAPISchema{Type: "string"}
	}
}
