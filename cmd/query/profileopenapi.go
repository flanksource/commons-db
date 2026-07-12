package main

import (
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
	store     *ProfileStore
}

func newProfileOpenAPIHandler(root *cobra.Command, config *rpc.Config, store *ProfileStore) http.Handler {
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
func mergeStoredProfiles(spec *rpc.OpenAPISpec, store *ProfileStore) error {
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

	profiles, err := store.List()
	if err != nil {
		return fmt.Errorf("load profile surfaces: %w", err)
	}
	for _, profile := range profiles {
		addProfileToSpec(spec, profile)
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

	parameters := make([]rpc.OpenAPIParameter, 0, len(profile.Params))
	for _, param := range profile.Params {
		parameters = append(parameters, profileParameter(param))
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
		},
	}}
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
	return rpc.OpenAPIParameter{
		Name:        param.Name,
		In:          "query",
		Description: param.Description,
		Required:    param.Required,
		Schema:      schema,
		Clicky:      &rpc.ClickyParameterMeta{Role: "filter"},
	}
}

func profileResponseSchema(profile query.Profile) *rpc.OpenAPISchema {
	properties := map[string]*rpc.OpenAPISchema{}
	idAssigned := false
	for _, column := range profile.Columns {
		if column.Hidden {
			continue
		}
		property := &rpc.OpenAPISchema{Type: columnJSONType(column.Type), Extensions: map[string]any{}}
		if column.Label != "" {
			property.Extensions["x-clicky-label"] = column.Label
		}
		if column.Format != "" {
			property.Extensions["x-clicky-format"] = column.Format
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
	if profile.Render != "" {
		item.Extensions = map[string]any{"x-clicky-render": profile.Render}
	}
	return &rpc.OpenAPISchema{Type: "array", Items: item}
}
