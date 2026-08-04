package profiles

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/flanksource/clicky/entity"
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
		if err := addProfileToSpec(spec, resolved.Profile); err != nil {
			return fmt.Errorf("add profile surface %q: %w", profile.Name, err)
		}
	}
	return nil
}

func addProfileToSpec(spec *rpc.OpenAPISpec, profile query.Profile) error {
	entityName := "profile-" + slugify(profile.Name)
	path := "/api/v1/profile/" + entityName
	spec.Clicky.Surfaces = append(spec.Clicky.Surfaces, rpc.ClickySurface{
		Key:         entityName,
		Entity:      entityName,
		Title:       profile.Name,
		Parent:      profileSurfaceParent,
		Description: "Run " + profile.Name,
		Icon:        providerIcon(profile.Provider.Type),
	})

	bindings, err := profile.ColumnFilterBindings()
	if err != nil {
		return err
	}
	parameters := make([]rpc.OpenAPIParameter, 0, len(profile.Params)+len(bindings)+2)
	filterKeys := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		filterKeys[binding.Column] = binding.Key
	}
	roles := map[query.ParamRole]bool{}
	for _, param := range profile.Params {
		parameters = append(parameters, profileParameter(spec, profile, param, path))
		roles[param.Role] = true
	}
	for _, binding := range bindings {
		filterName := profileFilterName(profile.Name, binding.Column)
		ensureProfileFilterComponent(spec, entity.FilterSpec{
			Name: filterName, Label: binding.Label, Type: "multi-filter", Multi: true,
			Source: entity.FilterSourceSpec{Kind: entity.SourceCustom},
		})
		parameters = append(parameters, rpc.OpenAPIParameter{
			Name: binding.Key, In: "query", Description: "Include or exclude " + binding.Label + " values",
			Schema: &rpc.OpenAPISchema{Type: "string", Title: binding.Label},
			Clicky: &rpc.ClickyParameterMeta{Role: "filter"},
			Lookup: &rpc.ClickyLookupMeta{
				Ref: "#/components/x-clicky-filters/" + filterName, URL: path,
				Filter: binding.Key, SearchParam: "__lookup_q", Multi: true,
			},
		})
	}
	if !roles[query.ParamRoleLimit] {
		limits := profile.RowLimits()
		parameters = append(parameters,
			rpc.OpenAPIParameter{
				Name: "limit", In: "query",
				Description: fmt.Sprintf("Rows per page (maximum %d); export up to %d rows with scope=all", limits.MaxPageSize, limits.MaxExportRows),
				Schema:      &rpc.OpenAPISchema{Type: "integer", Default: limits.PageSize},
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
	spec.Paths[path] = rpc.OpenAPIPath{"get": {
		Summary:     "Run " + profile.Name,
		Description: "Execute the stored query profile",
		OperationID: "run-" + entityName,
		Parameters:  parameters,
		Responses: map[string]rpc.OpenAPIResponse{
			"200": {
				Description: "Profile rows",
				Content: map[string]rpc.OpenAPIMediaType{
					"application/json": {Schema: profileResponseSchema(profile, filterKeys)},
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
	return nil
}

// ensureProfileFilterComponent is the single writer of the spec's filter
// components, so a column filter and a list param register through one path.
func ensureProfileFilterComponent(spec *rpc.OpenAPISpec, filter entity.FilterSpec) {
	if spec.Components == nil {
		spec.Components = &rpc.OpenAPIComponents{}
	}
	if spec.Components.ClickyFilters == nil {
		spec.Components.ClickyFilters = map[string]entity.FilterSpec{}
	}
	spec.Components.ClickyFilters[filter.Name] = filter
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

func profileParameter(spec *rpc.OpenAPISpec, profile query.Profile, param query.ParamDef, path string) rpc.OpenAPIParameter {
	schema := &rpc.OpenAPISchema{Type: "string", Title: param.DisplayLabel(), Description: param.Description}
	switch param.Type {
	case query.ParamTypeNumber:
		schema.Type = "number"
	case query.ParamTypeBoolean:
		schema.Type = "boolean"
	case query.ParamTypeDate:
		schema.Format = "date-time"
	}
	// A list travels as one comma-joined string, so its schema stays a string;
	// the allowed values live on the filter component the lookup points at.
	if param.Type != query.ParamTypeList {
		for _, option := range param.Options {
			schema.Enum = append(schema.Enum, option)
		}
	}
	if param.Default != nil {
		schema.Default = param.Default
	}
	role := string(param.Role)
	if role == "" {
		role = string(query.ParamRoleFilter)
	}
	parameter := rpc.OpenAPIParameter{
		Name:        param.Name,
		In:          "query",
		Description: param.Description,
		Required:    param.Required,
		Schema:      schema,
		Clicky:      &rpc.ClickyParameterMeta{Role: role},
	}
	if param.Type != query.ParamTypeList || param.Field == "" {
		return parameter
	}

	// A bound list is the same tri-state control a native column filter gets:
	// static options are inlined so the browser needs no round trip, and an
	// unenumerated one asks the provider for its distinct values.
	filterName := profileParamFilterName(profile.Name, param.Name)
	source := entity.FilterSourceSpec{Kind: entity.SourceCustom}
	lookup := &rpc.ClickyLookupMeta{
		Ref: "#/components/x-clicky-filters/" + filterName, URL: path,
		Filter: param.Name, SearchParam: "__lookup_q", Multi: true,
	}
	if len(param.Options) > 0 {
		options := make(map[string]string, len(param.Options))
		for _, option := range param.Options {
			options[option] = option
		}
		source = entity.FilterSourceSpec{Kind: entity.SourceStatic, Options: options}
		lookup.URL, lookup.SearchParam = "", ""
	}
	ensureProfileFilterComponent(spec, entity.FilterSpec{
		Name: filterName, Label: param.DisplayLabel(), Type: "multi-filter", Multi: true, Source: source,
	})
	parameter.Lookup = lookup
	if parameter.Description == "" {
		parameter.Description = "Include or exclude " + param.DisplayLabel() + " values"
	}
	return parameter
}

func profileResponseSchema(profile query.Profile, filterKeys map[string]string) *rpc.OpenAPISchema {
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
		if column.Unit != "" {
			property.Extensions["x-clicky-unit"] = column.Unit
		}
		if column.Kind != "" {
			property.Extensions["x-clicky-kind"] = string(column.Kind)
		}
		if key := filterKeys[column.Name]; key != "" {
			property.Extensions["x-clicky-filter-key"] = key
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
