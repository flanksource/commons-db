package profiles

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/rpc"
	"github.com/flanksource/commons-db/query"
	"github.com/spf13/cobra"
)

type profileOpenAPIHandler struct {
	root       *cobra.Command
	config     *rpc.Config
	generator  *rpc.OpenAPIGenerator
	store      Store
	extensions []OpenAPIExtension
	mu         sync.Mutex
	// Documents already encoded for the profiles named by fingerprint. Cleared
	// whenever that fingerprint changes, so a cached body is never served for a
	// different set of profiles than the one it was built from.
	fingerprint string
	documents   map[openAPIRepresentation]openAPIDocument
}

type OpenAPIExtension func(*rpc.OpenAPISpec)

func newProfileOpenAPIHandler(root *cobra.Command, config *rpc.Config, store Store, extensions []OpenAPIExtension) http.Handler {
	return &profileOpenAPIHandler{
		root:   root,
		config: config,
		generator: rpc.NewOpenAPIGenerator(&rpc.OpenAPIConfig{
			Title: "Query", Description: "Connections, profiles and execution", Version: "0.1.0",
		}),
		store:      store,
		extensions: extensions,
	}
}

func (h *profileOpenAPIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", http.MethodGet+", "+http.MethodOptions)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	representation := negotiateOpenAPIRepresentation(r.Header.Get("Accept"))
	document, err := h.document(r.Context(), representation)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", document.contentType)
	w.Header().Set("ETag", document.etag)
	// The document changes whenever a profile does, so it is never served
	// unconditionally — but a revalidation that matches costs a round trip
	// instead of half a megabyte.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Add("Vary", "Accept")
	if matchesETag(r.Header.Get("If-None-Match"), document.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if _, err := w.Write(document.body); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// document returns the requested representation, regenerating only when the
// stored profiles have changed since the cached one was built.
func (h *profileOpenAPIHandler) document(ctx context.Context, representation openAPIRepresentation) (openAPIDocument, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	profiles, err := h.store.List(ctx)
	if err != nil {
		return openAPIDocument{}, fmt.Errorf("load profile surfaces: %w", err)
	}
	fingerprint, err := fingerprintProfiles(profiles)
	if err != nil {
		return openAPIDocument{}, err
	}
	if fingerprint != h.fingerprint {
		h.fingerprint, h.documents = fingerprint, map[openAPIRepresentation]openAPIDocument{}
	}
	if document, ok := h.documents[representation]; ok {
		return document, nil
	}

	spec, err := h.generator.GenerateFromCobraWithConfig(h.root, h.config)
	if err != nil {
		return openAPIDocument{}, fmt.Errorf("generate OpenAPI: %v", err)
	}
	if err := mergeStoredProfiles(ctx, spec, newSnapshotStore(profiles)); err != nil {
		return openAPIDocument{}, err
	}
	for _, extend := range h.extensions {
		extend(spec)
	}
	document, err := encodeOpenAPIDocument(spec, representation)
	if err != nil {
		return openAPIDocument{}, err
	}
	h.documents[representation] = document
	return document, nil
}

// A conditional request may carry several tags, and a proxy may weaken one.
func matchesETag(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

// mergeStoredProfiles replaces startup-snapshotted profile surfaces with a
// fresh view of the YAML store. The resulting operations execute through the
// generic /profile/profile-<slug> handler, so no live Cobra mutation is needed.
func mergeStoredProfiles(ctx context.Context, spec *rpc.OpenAPISpec, store Store) error {
	if spec.Clicky == nil {
		spec.Clicky = &rpc.ClickySpecMeta{}
	}
	dropProfileFilterComponents(spec)
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

	profiles, err := store.List(ctx)
	if err != nil {
		return fmt.Errorf("load profile surfaces: %w", err)
	}
	for _, profile := range profiles {
		resolved, err := ResolveWithoutTouch(ctx, store, profile.Name)
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
	runtimeBindings, err := profile.RuntimeFilterBindings()
	if err != nil {
		return err
	}
	bindings = append(bindings, runtimeBindings...)
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
		filterOwner := binding.Column
		if filterOwner == "" {
			filterOwner = binding.Key
		}
		filterName := profileFilterName(profile.Name, filterOwner)
		ensureProfileFilterComponent(spec, entity.FilterSpec{
			Name: filterName, Label: binding.Label,
			Type: binding.ControlType(), Multi: binding.Multi,
			Source: entity.FilterSourceSpec{Kind: entity.SourceCustom},
		})
		schema := &rpc.OpenAPISchema{Type: "string", Title: binding.Label}
		for _, option := range binding.Options {
			schema.Enum = append(schema.Enum, option)
		}
		// A generated control's default is the selection the server applies when
		// the request names none, so it belongs in the contract rather than only
		// in the resolver — a caller reading the spec must see the query they
		// will actually get.
		if binding.Default != "" {
			schema.Default = binding.Default
		}
		parameters = append(parameters, rpc.OpenAPIParameter{
			Name: binding.Key, In: "query",
			Description: profileFilterDescription(binding),
			Schema:      schema,
			Clicky:      &rpc.ClickyParameterMeta{Role: "filter"},
			Lookup:      profileFilterLookupMeta(filterName, binding.Key, path, binding.Multi, binding.Lookup),
		})
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
	if !roles[query.ParamRoleOffset] && profile.Pageable() == nil &&
		query.SupportsPaging(profile.Provider.Type).Supports(query.PagingOffset) {
		parameters = append(parameters,
			rpc.OpenAPIParameter{
				Name: "offset", In: "query", Description: "Rows to skip",
				Schema: &rpc.OpenAPISchema{Type: "integer", Default: 0},
				Clicky: &rpc.ClickyParameterMeta{Role: "offset"},
			})
	}
	// Sorting is offered as a pair — which column, and which direction — because
	// that is the shape the table's sort control is built from; either one alone
	// describes half a sort and produces no control at all.
	//
	// It is offered only where it would actually be applied. A buffered read
	// slices a result the provider produced in its own order, so a sort there
	// would reorder nothing while claiming otherwise; SortBindings has already
	// refused the providers that ignore an order, and streamable settles the
	// profiles whose pipeline needs every row before any row is correct.
	sortable, err := profile.SortBindings()
	if err != nil {
		return err
	}
	streamable, err := profile.Streamable()
	if err != nil {
		return err
	}
	if len(sortable) > 0 && streamable && query.PagesNatively(profile.Provider.Type) &&
		!roles[query.ParamRoleSort] && !roles[query.ParamRoleOrder] {
		columns := make([]any, 0, len(sortable))
		for _, binding := range sortable {
			columns = append(columns, binding.Column)
		}
		parameters = append(parameters,
			rpc.OpenAPIParameter{
				Name: "sort", In: "query",
				Description: "Column to order rows by; it leads the profile's own order, whose tiebreaker still ends it",
				Schema:      &rpc.OpenAPISchema{Type: "string", Enum: columns},
				Clicky:      &rpc.ClickyParameterMeta{Role: "sort"},
			},
			rpc.OpenAPIParameter{
				Name: "order", In: "query",
				Description: "Direction for sort",
				Schema:      &rpc.OpenAPISchema{Type: "string", Enum: []any{"asc", "desc"}, Default: "asc"},
				Clicky:      &rpc.ClickyParameterMeta{Role: "order"},
			})
	}
	// A cursor is only offered by a profile that can actually serve one: it
	// needs a total order to name a position in, and a provider that resumes
	// from one. Advertising it otherwise would put the UI into cursor mode
	// against a server that refuses every cursor it sends.
	if profile.Pageable() == nil &&
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

// profileFilterLookupMeta names a filter's shape component, and — only when the
// backend can enumerate it — where to fetch its options.
//
// The two are separate concerns that happen to travel in one object. Ref is the
// control's shape, which every generated filter has and which never varies with
// the values currently selected, so it is emitted unconditionally: a client that
// reads shapes from the spec renders the right control on first paint and keeps
// it across a filter change. URL and SearchParam are the option source, and a
// range has no list to offer — pointing one at a lookup would advertise a list
// that has no answer.
func profileFilterLookupMeta(filterName, key, path string, multi, enumerable bool) *rpc.ClickyLookupMeta {
	lookup := &rpc.ClickyLookupMeta{
		Ref:    "#/components/x-clicky-filters/" + filterName,
		Filter: key, Multi: multi,
	}
	if enumerable {
		lookup.URL, lookup.SearchParam = path, "__lookup_q"
	}
	return lookup
}

// profileFilterDescription says what this filter's wire value means, which
// differs by kind: a value selection takes a list, a range takes bounds.
func profileFilterDescription(binding query.ColumnFilterBinding) string {
	switch binding.Kind.Normalized() {
	case query.ColumnFilterKindRange:
		return "Bound " + binding.Label + " with >=, >, <= or < (e.g. \">=100,<500\")"
	case query.ColumnFilterKindDuration:
		// Naming the unit is what tells an operator which number a bare bound is,
		// since one written without a suffix is taken as already being in it.
		return "Bound " + binding.Label + " with >=, >, <= or < using a duration or a number of " +
			cmp.Or(binding.Unit, api.ColumnUnitMilliseconds) + " (e.g. \">=500ms,<5s\")"
	case query.ColumnFilterKindTime:
		return "Bound " + binding.Label + " with >=, >, <= or < using a time or date math (e.g. \">=now-1h\")"
	case query.ColumnFilterKindDate:
		return "Bound " + binding.Label + " with >=, >, <= or < using a date or date math (e.g. \">=now-7d\")"
	case query.ColumnFilterKindBoolean:
		return "Restrict " + binding.Label + " to true or false"
	case query.ColumnFilterKindText:
		return "Match " + binding.Label + " by substring; prefix a value with ! to exclude it"
	case query.ColumnFilterKindExact:
		return "Match " + binding.Label + " exactly against values you type; prefix a value with ! to exclude it"
	case query.ColumnFilterKindWorkload:
		return "Select one workload inside the profile target scope"
	case query.ColumnFilterKindLabels:
		return "Narrow the profile target scope by Kubernetes labels"
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
		schema.Format = "date"
	case query.ParamTypeDateTime:
		schema.Format = "date-time"
	}
	// A list travels as one comma-joined string, so its schema stays a string;
	// the allowed values live on the filter component the lookup points at.
	if param.Type != query.ParamTypeList && param.Type != query.ParamTypeLabels {
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
	if (param.Type != query.ParamTypeList && param.Type != query.ParamTypeLabels) || param.Field == "" {
		return parameter
	}

	// A bound list is the same tri-state control a native column filter gets:
	// static options are inlined so the browser needs no round trip, and an
	// unenumerated one asks the provider for its distinct values.
	filterName := profileParamFilterName(profile.Name, param.Name)
	source := entity.FilterSourceSpec{Kind: entity.SourceCustom}
	// Options declared in the profile are inlined into the component, so the
	// browser already holds the whole set and has nothing to ask a lookup for.
	enumerable := len(param.Options) == 0
	if !enumerable {
		options := make(map[string]string, len(param.Options))
		for _, option := range param.Options {
			options[option] = option
		}
		source = entity.FilterSourceSpec{Kind: entity.SourceStatic, Options: options}
	}
	lookup := profileFilterLookupMeta(filterName, param.Name, path, true, enumerable)
	controlType := "multi-filter"
	if param.Type == query.ParamTypeLabels {
		controlType = "labels"
	}
	ensureProfileFilterComponent(spec, entity.FilterSpec{
		Name: filterName, Label: param.DisplayLabel(), Type: controlType, Multi: true, Source: source,
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
