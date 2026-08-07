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
		Path:        profileSurfacePath(profile.Name),
		Description: "Run " + profile.Name,
		Icon:        profileIcon(profile),
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
			Name: filterName, Label: binding.Label,
			Type: binding.Kind.ControlType(), Multi: binding.Multi,
			Source: entity.FilterSourceSpec{Kind: entity.SourceCustom},
		})
		schema := &rpc.OpenAPISchema{Type: "string", Title: binding.Label}
		for _, option := range binding.Options {
			schema.Enum = append(schema.Enum, option)
		}
		parameter := rpc.OpenAPIParameter{
			Name: binding.Key, In: "query",
			Description: profileFilterDescription(binding),
			Schema:      schema,
			Clicky:      &rpc.ClickyParameterMeta{Role: "filter"},
		}
		// Only a filter the backend can enumerate gets a lookup URL; pointing one
		// at a range would advertise a list that has no answer.
		if binding.Lookup {
			parameter.Lookup = &rpc.ClickyLookupMeta{
				Ref: "#/components/x-clicky-filters/" + filterName, URL: path,
				Filter: binding.Key, SearchParam: "__lookup_q", Multi: binding.Multi,
			}
		}
		parameters = append(parameters, parameter)
	}
	if !roles[query.ParamRoleLimit] {
		limits := profile.RowLimits()
		parameters = append(parameters,
			rpc.OpenAPIParameter{
				Name: "limit", In: "query",
				Description: fmt.Sprintf("Rows per page (maximum %d); export up to %d rows with scope=all", limits.MaxPageSize, limits.MaxExportRows),
				Schema:      &rpc.OpenAPISchema{Type: "integer", Default: limits.PageSize},
				Clicky:      &rpc.ClickyParameterMeta{Role: "limit"},
			})
	}
	// An offset names a position, and a position is only meaningful under a total
	// order — the same rule the cursor below is held to, and the same one
	// ExecutePages enforces when the request arrives. A profile that cannot page
	// still takes a limit: capping rows needs no order.
	if !roles[query.ParamRoleOffset] && profile.Pageable() == nil {
		parameters = append(parameters,
			rpc.OpenAPIParameter{
				Name: "offset", In: "query", Description: "Rows to skip",
				Schema: &rpc.OpenAPISchema{Type: "integer", Default: 0},
				Clicky: &rpc.ClickyParameterMeta{Role: "offset"},
			})
	}
	// A cursor is only offered by a profile that can actually serve one: it
	// needs a total order to name a position in, and a provider that resumes
	// from one. Advertising it otherwise would put the UI into cursor mode
	// against a server that refuses every cursor it sends.
	if !roles[query.ParamRoleCursor] &&
		profile.Pageable() == nil &&
		query.SupportsPaging(profile.Provider.Type).Supports(query.PagingCursor) {
		parameters = append(parameters,
			rpc.OpenAPIParameter{
				Name: "cursor", In: "query",
				Description: "Opaque position from the previous page's X-Next-Cursor; resumes after it",
				Schema:      &rpc.OpenAPISchema{Type: "string"},
				Clicky:      &rpc.ClickyParameterMeta{Role: "cursor"},
			})
	}
	spec.Paths[path] = rpc.OpenAPIPath{"get": {
		Summary:     "Run " + profile.Name,
		Description: "Execute the stored query profile",
		OperationID: "run-" + entityName,
		Parameters:  parameters,
		Responses: map[string]rpc.OpenAPIResponse{
			"200": {
				Description: "Profile rows. Paging travels in the response headers, not the body.",
				Headers:     exportResponseHeaders(),
				Content: map[string]rpc.OpenAPIMediaType{
					// The shape, not merely the encoding, is negotiated: the
					// interactive table asks for the clicky envelope and every
					// other format streams a bare sequence of rows. Declaring
					// only one of the two leaves any generated client wrong
					// against whichever it did not get.
					"application/json":        {Schema: profileResponseSchema(profile, filterKeys)},
					"application/json+clicky": {Schema: clickyDocumentSchema()},
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

// profileFilterDescription says what this filter's wire value means, which
// differs by kind: a value selection takes a list, a range takes bounds.
func profileFilterDescription(binding query.ColumnFilterBinding) string {
	switch binding.Kind.Normalized() {
	case query.ColumnFilterKindRange:
		return "Bound " + binding.Label + " with >=, >, <= or < (e.g. \">=100,<500\")"
	case query.ColumnFilterKindTime:
		return "Bound " + binding.Label + " with >=, >, <= or < using a time or date math (e.g. \">=now-1h\")"
	case query.ColumnFilterKindBoolean:
		return "Restrict " + binding.Label + " to true or false"
	case query.ColumnFilterKindText:
		return "Match " + binding.Label + " by substring; prefix a value with ! to exclude it"
	default:
		return "Include or exclude " + binding.Label + " values; prefix a value with ! to exclude it"
	}
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
	// Every provider can serve every row now — one without native paging is
	// paged by slicing a buffered result. What differs is the cost, which is
	// what AllRowsMode names.
	meta.Scopes = append(meta.Scopes, "all")
	if streamable, err := profile.Streamable(); err == nil && streamable {
		meta.AllRowsMode = "streaming"
	} else {
		meta.AllRowsMode = "buffered"
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

// clickyDocumentSchema describes the envelope returned for
// application/json+clicky: the rendered table clicky-ui's <Clicky> consumes,
// rather than the bare rows every other format streams.
//
// It is described to the depth a caller needs to dispatch on — the node tree is
// recursive and open, so pinning every kind here would be a second, staler copy
// of the renderer's own contract.
func clickyDocumentSchema() *rpc.OpenAPISchema {
	node := &rpc.OpenAPISchema{
		Type:        "object",
		Description: "Rendered node tree; `kind` selects how to read it (table, text, list, map, tree, ...)",
		Properties: map[string]*rpc.OpenAPISchema{
			"kind":    {Type: "string"},
			"columns": {Type: "array", Items: &rpc.OpenAPISchema{Type: "object"}},
			"rows": {Type: "array", Items: &rpc.OpenAPISchema{
				Type:       "object",
				Properties: map[string]*rpc.OpenAPISchema{"cells": {Type: "object"}},
			}},
		},
	}
	return &rpc.OpenAPISchema{
		Type: "object",
		Properties: map[string]*rpc.OpenAPISchema{
			"version": {Type: "integer", Description: "Envelope version"},
			"node":    node,
		},
	}
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
