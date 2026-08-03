package profiles

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/clicky/rpc"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/spf13/cobra"
)

type StoreProvider func() (Store, error)
type ContextProvider func() dbcontext.Context
type BodyDecoder func(context.Context, map[string]any) (map[string]any, error)

type Options struct {
	Store      StoreProvider
	Context    ContextProvider
	DecodeBody BodyDecoder
}

type Service struct {
	store      StoreProvider
	context    ContextProvider
	decodeBody BodyDecoder
	mu         sync.Mutex
	registered map[string]struct{}
}

func New(options Options) (*Service, error) {
	if options.Store == nil {
		return nil, fmt.Errorf("store provider is required")
	}
	if options.Context == nil {
		return nil, fmt.Errorf("context provider is required")
	}
	if options.DecodeBody == nil {
		return nil, fmt.Errorf("body decoder is required")
	}
	return &Service{
		store: options.Store, context: options.Context, decodeBody: options.DecodeBody,
		registered: map[string]struct{}{},
	}, nil
}

// profileSurfaceParent groups every per-profile dynamic entity under one sidebar
// section. It is the x-clicky-parent of each generated profile entity.
const profileSurfaceParent = "profiles"

// profileItem adapts a query.Profile to clicky's EntityItem. The embedded Profile
// is promoted in JSON, so list/get responses carry the full definition (used by
// the UI to pre-fill the edit form).
type profileItem struct {
	query.Profile
}

func (p profileItem) GetID() string   { return p.Name }
func (p profileItem) GetName() string { return p.Name }

// Columns implements api.TableProvider so the profiles list renders as a clicky
// table of name + provider type (the "connection type") + the referenced
// connection and a truncated query, on both the CLI and the web /profiles
// surface.
func (p profileItem) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("name").Label("Name").Style("font-bold").Build(),
		api.Column("type").Label("Type").Build(),
		api.Column("connection").Label("Connection").Style("text-muted").Build(),
		api.Column("query").Label("Query").MaxWidth(60).Style("text-muted").Build(),
	}
}

// Row implements api.TableProvider, returning the cell values for Columns.
func (p profileItem) Row() map[string]any {
	return map[string]any{
		"name":       p.Name,
		"type":       p.Provider.Type,
		"connection": p.Provider.Connection,
		"query":      p.Query,
	}
}

// profileListOpts are the (currently empty) list options for the profile entity.
type profileListOpts struct{}

// registerProfileEntity registers the YAML-backed profile entity with list +
// full CRUD on the CLI and over HTTP. Create/Update use the context-aware
// handlers so the nested profile body (provider/params/columns) survives via
// rpc.RequestFromContext instead of the executor's flag-flattening. Execution
// (GET /{name}?params) is served by execHandler and schemas by schemaHandler.
func (s *Service) RegisterClicky() {
	clicky.NewEntity[profileItem, profileListOpts, profileItem]("profiles").
		List(func(profileListOpts) ([]profileItem, error) {
			store, err := s.store()
			if err != nil {
				return nil, err
			}
			ps, err := store.List(context.Background())
			if err != nil {
				return nil, err
			}
			items := make([]profileItem, len(ps))
			for i, p := range ps {
				items[i] = profileItem{p}
			}
			return items, nil
		}).
		Get(func(id string) (profileItem, error) {
			p, err := s.Get(context.Background(), id)
			if err != nil {
				return profileItem{}, err
			}
			return profileItem{p}, nil
		}).
		CreateWithContext(func(ctx context.Context, body map[string]any) (profileItem, error) {
			profile, err := s.Save(ctx, body, "")
			return profileItem{profile}, err
		}).
		UpdateWithContext(func(ctx context.Context, id string, body map[string]any) (profileItem, error) {
			profile, err := s.Save(ctx, body, id)
			return profileItem{profile}, err
		}).
		DeleteWithContext(func(ctx context.Context, id string) error {
			return s.Delete(ctx, id)
		}).
		Register()
}

// saveProfile decodes the (nested) request body into a Profile and persists it.
// On update, a path id supplies the profile name when the body omits it.
func (s *Service) Save(ctx context.Context, body map[string]any, id string) (query.Profile, error) {
	b, err := s.decodeBody(ctx, body)
	if err != nil {
		return query.Profile{}, err
	}
	b = cloneBody(b)
	replaceExisting, err := updateReplaceExisting(b)
	if err != nil {
		return query.Profile{}, err
	}
	if id == "" && replaceExisting {
		return query.Profile{}, fmt.Errorf("replaceExisting is only valid when updating a profile")
	}
	p, err := profileFromBody(b)
	if err != nil && id != "" {
		b["profile"] = id
		p, err = profileFromBody(b)
	}
	if err != nil {
		return query.Profile{}, err
	}
	store, err := s.store()
	if err != nil {
		return query.Profile{}, err
	}
	if id != "" {
		err = store.Update(ctx, id, p, UpdateOptions{ReplaceExisting: replaceExisting})
	} else {
		err = store.Save(ctx, p)
	}
	if err != nil {
		return query.Profile{}, err
	}
	return p, nil
}

func cloneBody(body map[string]any) map[string]any {
	cloned := make(map[string]any, len(body))
	for key, value := range body {
		cloned[key] = value
	}
	return cloned
}

func updateReplaceExisting(body map[string]any) (bool, error) {
	raw, exists := body["replaceExisting"]
	delete(body, "replaceExisting")
	if !exists {
		return false, nil
	}
	replace, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("replaceExisting must be a boolean")
	}
	return replace, nil
}

func (s *Service) List(ctx context.Context) ([]query.Profile, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	return store.List(ctx)
}

func (s *Service) Delete(ctx context.Context, name string) error {
	store, err := s.store()
	if err != nil {
		return err
	}
	return store.Delete(ctx, name)
}

// registerProfileEntities registers one clicky dynamic entity per stored profile
// so each profile appears as its own sidebar surface (with a provider-derived
// icon) and is executable: the entity's List runs the profile and returns its
// rows. Entities are snapshotted at startup; a newly created profile needs a
// restart to appear as its own surface (it still executes via execHandler and
// shows in the aggregate list until then).
func (s *Service) RegisterDynamic(ctx context.Context) error {
	store, err := s.store()
	if err != nil {
		return err
	}
	profiles, err := store.List(ctx)
	if err != nil {
		return err
	}
	for _, p := range profiles {
		name := p.Name
		resolved, err := Resolve(ctx, store, name)
		if err != nil {
			return err
		}
		bindings, err := resolved.Profile.ColumnFilterBindings()
		if err != nil {
			return fmt.Errorf("build filters for profile %q: %w", name, err)
		}
		schemaJSON, err := profileEntitySchema(resolved.Profile)
		if err != nil {
			return fmt.Errorf("build entity schema for profile %q: %w", name, err)
		}
		if !s.markRegistered(name) {
			continue
		}
		for _, binding := range bindings {
			filterName := profileFilterName(resolved.Profile.Name, binding.Column)
			if _, exists := entity.GetFilter(filterName); exists {
				s.unmarkRegistered(name)
				return fmt.Errorf("profile filter %q is already registered", filterName)
			}
		}
		for _, binding := range bindings {
			filterName := profileFilterName(resolved.Profile.Name, binding.Column)
			entity.RegisterFilter(entity.NamedFilter{
				Name: filterName, Label: binding.Label, Type: "multi-filter", Multi: true,
				Source: profileFilterSource{service: s, profileName: name, key: binding.Key},
			})
		}
		entity.NewDynamicEntity("profile-"+slugify(name), schemaJSON).
			List(func(_ context.Context, opts map[string]string) ([]map[string]any, error) {
				store, err := s.store()
				if err != nil {
					return nil, err
				}
				live, err := Resolve(context.Background(), store, name)
				if err != nil {
					return nil, err
				}
				// The base profile flow needs no database; only postgres/sqlite
				// processors do. The context provider supplies the DB-backed
				// context under `serve` and a DB-less one on the CLI.
				res, err := query.Execute(s.context(), live.Profile, toParams(opts))
				if err != nil {
					return nil, err
				}
				return res.Rows, nil
			}).
			Register()
	}
	return nil
}

func (s *Service) markRegistered(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	slug := slugify(name)
	if _, ok := s.registered[slug]; ok {
		return false
	}
	s.registered[slug] = struct{}{}
	return true
}

func (s *Service) unmarkRegistered(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.registered, slugify(name))
}

func (s *Service) Get(ctx context.Context, name string) (query.Profile, error) {
	store, err := s.store()
	if err != nil {
		return query.Profile{}, err
	}
	return store.Get(ctx, name)
}

func (s *Service) Handler(prefix string, next http.Handler) (http.Handler, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	return newExecHandler(prefix, s.context(), store, newProfileSampleHandler(prefix, s.context(), next)), nil
}

func (s *Service) OpenAPIHandler(root *cobra.Command, config *rpc.Config) (http.Handler, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	return newProfileOpenAPIHandler(root, config, store), nil
}

// profileEntitySchema builds the dynamic-entity JSON schema for a profile: its
// visible columns become the entity properties (the first is the id/name key),
// grouped under the profiles surface and tagged with the provider icon. A
// column-less profile gets a synthesized id property so the schema is valid;
// rows still render via the map-backed dynamic item.
func profileEntitySchema(p query.Profile) ([]byte, error) {
	bindings, err := p.ColumnFilterBindings()
	if err != nil {
		return nil, err
	}
	filterByColumn := make(map[string]query.ColumnFilterBinding, len(bindings))
	for _, binding := range bindings {
		filterByColumn[binding.Column] = binding
	}
	props := map[string]any{}
	idAssigned := false
	for _, c := range p.Columns {
		if c.Hidden {
			continue
		}
		prop := columnJSONSchema(c.Type)
		if c.Type != "" {
			prop["x-clicky-type"] = string(c.Type)
		}
		if c.Label != "" {
			prop["x-clicky-label"] = c.Label
		}
		if c.Format != "" {
			prop["x-clicky-format"] = c.Format
		}
		if c.Unit != "" {
			prop["x-clicky-unit"] = c.Unit
		}
		if binding, ok := filterByColumn[c.Name]; ok {
			prop["x-clicky-filter"] = profileFilterName(p.Name, c.Name)
			prop["x-clicky-filter-key"] = binding.Key
		}
		if !idAssigned {
			prop["x-clicky-id"] = true
			prop["x-clicky-name"] = true
			idAssigned = true
		}
		props[c.Name] = prop
	}
	if !idAssigned {
		props["id"] = map[string]any{"type": "string", "x-clicky-id": true, "x-clicky-name": true}
	}
	doc := map[string]any{
		"type":            "object",
		"properties":      props,
		"x-clicky-parent": profileSurfaceParent,
		"x-clicky-icon":   providerIcon(p.Provider.Type),
		"x-clicky-title":  p.Name,
	}
	// x-clicky-render lets the frontend pick a presentation (e.g. the LogsTable
	// view for trace/log profiles, the session-backed trace/top views) instead
	// of the default data table.
	if render := p.RenderMode(); render != "" {
		doc["x-clicky-render"] = render
	}
	return json.Marshal(doc)
}

func profileFilterName(profileName, columnName string) string {
	digest := sha256.Sum256([]byte(columnName))
	return fmt.Sprintf("profile-%s-column-%x", slugify(profileName), digest[:6])
}

type profileFilterSource struct {
	service     *Service
	profileName string
	key         string
}

func (source profileFilterSource) Options(fc entity.FilterContext, search string, limit int) (map[string]api.Textable, int, error) {
	store, err := source.service.store()
	if err != nil {
		return nil, 0, err
	}
	resolved, err := Resolve(fc.Ctx(), store, source.profileName)
	if err != nil {
		return nil, 0, err
	}
	options, total, err := query.LookupFilterValues(
		source.service.context().Wrap(fc.Ctx()), resolved.Profile, filterLookupParams(resolved.Profile, fc.Params), source.key, search, limit,
	)
	if err != nil {
		return nil, 0, err
	}
	result := make(map[string]api.Textable, len(options))
	for _, option := range options {
		result[option.Value] = api.Text{Content: option.Value}
	}
	return result, total, nil
}

func filterLookupParams(profile query.Profile, values map[string]string) map[string]any {
	allowed := make(map[string]bool, len(profile.Params))
	for _, parameter := range profile.Params {
		allowed[parameter.Name] = true
	}
	params := make(map[string]any, len(values))
	for key, value := range values {
		if allowed[key] || strings.HasPrefix(key, "filter.") {
			params[key] = value
		}
	}
	return params
}

func (source profileFilterSource) Resolve(_ entity.FilterContext, values []string) (map[string]api.Textable, error) {
	resolved := make(map[string]api.Textable, len(values))
	for _, value := range values {
		resolved[value] = api.Text{Content: value}
	}
	return resolved, nil
}

// columnJSONSchema maps a profile ColumnType to its preferred JSON shape.
func columnJSONSchema(t query.ColumnType) map[string]any {
	switch t {
	case query.ColumnTypeNumber:
		return map[string]any{"type": "number"}
	case query.ColumnTypeBoolean:
		return map[string]any{"type": "boolean"}
	case query.ColumnTypeKeyValue:
		return map[string]any{"type": "object", "additionalProperties": map[string]any{}}
	case query.ColumnTypeKeyValues:
		return map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"key":   map[string]any{"type": "string"},
					"value": map[string]any{},
				},
				"required": []string{"key", "value"},
			},
		}
	case query.ColumnTypeJSON:
		return map[string]any{"oneOf": []any{
			map[string]any{"type": "object"},
			map[string]any{"type": "array"},
			map[string]any{"type": "string"},
			map[string]any{"type": "number"},
			map[string]any{"type": "boolean"},
			map[string]any{"type": "null"},
		}}
	default:
		return map[string]any{"type": "string"}
	}
}

// toParams converts the request flag map to the params map query.Execute expects.
func toParams(opts map[string]string) map[string]any {
	params := make(map[string]any, len(opts))
	for k, v := range opts {
		params[k] = v
	}
	return params
}

// profileFromBody decodes a request body into a Profile, failing fast on a
// missing name.
func profileFromBody(body map[string]any) (query.Profile, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return query.Profile{}, fmt.Errorf("encode profile body: %w", err)
	}
	var p query.Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return query.Profile{}, fmt.Errorf("invalid profile: %w", err)
	}
	if p.Name == "" {
		return query.Profile{}, fmt.Errorf("profile name is required")
	}
	return p, nil
}
