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

// DecodeRequestBody is the BodyDecoder for a service hosted by clicky's RPC
// layer: it reads the in-flight request's JSON body, falling back to the
// arguments clicky already decoded when there is no request in context (a CLI
// invocation, or a call made directly in a test).
//
// Every embedder needs exactly this, so it ships here rather than being
// reimplemented — slightly differently — in each one.
func DecodeRequestBody(ctx context.Context, fallback map[string]any) (map[string]any, error) {
	request, ok := rpc.RequestFromContext(ctx)
	if !ok || request.Body == nil {
		return fallback, nil
	}
	var body map[string]any
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode request body: %w", err)
	}
	return body, nil
}

type Options struct {
	Store             StoreProvider
	Context           ContextProvider
	DecodeBody        BodyDecoder
	Snapshots         SnapshotService
	OpenAPIExtensions []OpenAPIExtension
}

type Service struct {
	store             StoreProvider
	context           ContextProvider
	decodeBody        BodyDecoder
	snapshots         SnapshotService
	openAPIExtensions []OpenAPIExtension
	mu                sync.Mutex
	registered        map[string]struct{}
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
		snapshots:         options.Snapshots,
		openAPIExtensions: append([]OpenAPIExtension(nil), options.OpenAPIExtensions...),
		registered:        map[string]struct{}{},
	}, nil
}

func (s *Service) SetSnapshots(service SnapshotService) {
	s.snapshots = service
}

// profileSurfaceParent groups every per-profile dynamic entity under one sidebar
// section. It is the x-clicky-parent of each generated profile entity.
const profileSurfaceParent = "profiles"

// profilePathDelimiters are the characters a profile name uses to encode its
// place in the hierarchy: `jms.incoming.disbursements` and `logs/api` both nest
// three and two levels deep. A hyphen is deliberately absent — it is an ordinary
// name character, and splitting on it would shatter `remote-debugger`.
const profilePathDelimiters = "./"

// profileSurfacePath is the x-clicky-path a profile surface carries: its name
// split on the delimiters above and rejoined with clicky's wire separator, so
// the frontend nests the sidebar without knowing this convention exists.
func profileSurfacePath(name string) string {
	return entity.JoinPath(entity.SplitPath(name, profilePathDelimiters))
}

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
		api.Column("access").Label("Access").Build(),
		api.Column("expires").Label("Expires").Style("text-muted").Build(),
		api.Column("query").Label("Query").MaxWidth(60).Style("text-muted").Build(),
	}
}

// Row implements api.TableProvider, returning the cell values for Columns.
func (p profileItem) Row() map[string]any {
	return map[string]any{
		"name":       p.Name,
		"type":       p.Provider.Type,
		"connection": p.Provider.Connection,
		"access":     profileAccess(p.Profile),
		"expires":    p.ExpiresAt,
		"query":      p.Query,
	}
}

func profileAccess(profile query.Profile) string {
	if profile.ReadOnly {
		return "read-only"
	}
	return "editable"
}

// profileListOpts are the (currently empty) list options for the profile entity.
type profileListOpts struct{}

// registerProfileEntity registers the YAML-backed profile entity with list +
// full CRUD on the CLI and over HTTP. Create/Update use the context-aware
// handlers so the nested profile body (provider/params/columns) survives via
// rpc.RequestFromContext instead of the executor's flag-flattening. Execution
// (GET /{name}?params) is served by execHandler and schemas by schemaHandler.
//
// It also registers the profile family, which is the route each individual
// profile answers on — see RegisterFamily. The family reads the store through
// the same provider this service does, so swapping the YAML store for the
// database one at serve time needs no re-registration.
func (s *Service) RegisterClicky() {
	s.RegisterFamily()
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
		WithAction(entity.ActionWithFlagsAndContext("replay", ReplayFlags{},
			func(ctx context.Context, id string, flagMap map[string]string) (ReplayResult, error) {
				options, err := decodeActionFlags[ReplayFlags](flagMap)
				if err != nil {
					return ReplayResult{}, err
				}
				return s.Replay(ctx, id, options)
			}).
			WithShort("Turn one result row back into its outbound HTTP request and preview or send it").
			// Replaying drives a real side effect into a real system, so the
			// action asks even where the app's default policy would not.
			WithToolPermission(entity.ToolPermissionAsk)).
		WithAction(entity.ActionWithFlagsAndContext("reconcile", ReconcileFlags{},
			func(ctx context.Context, id string, flagMap map[string]string) (ReconcileSnapshotDescriptor, error) {
				options, err := decodeActionFlags[ReconcileFlags](flagMap)
				if err != nil {
					return ReconcileSnapshotDescriptor{}, err
				}
				return s.ReconcileSnapshot(ctx, id, options)
			}).
			WithShort("Join two profiles and materialize an expiring result snapshot")).
		WithAction(entity.ActionWithFlagsAndContext("reconcile-materialize", ReconcileMaterializeOptions{},
			func(ctx context.Context, _ string, flagMap map[string]string) (ReconcileSnapshotDescriptor, error) {
				options, err := decodeActionFlags[ReconcileMaterializeOptions](flagMap)
				if err != nil {
					return ReconcileSnapshotDescriptor{}, err
				}
				return s.MaterializeReconcile(ctx, options)
			}).WithShort("Materialize transformed or projected reconciliation rows")).
		WithAction(entity.ActionWithFlagsAndContext("run", RunFlags{},
			func(ctx context.Context, id string, flagMap map[string]string) (*RunResult, error) {
				options, err := decodeActionFlags[RunFlags](flagMap)
				if err != nil {
					return nil, err
				}
				return s.Run(ctx, id, options)
			}).
			WithShort("Read one page of this profile, or every page with --all")).
		Filters(profileFilter{service: s}).
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

// RegisterDynamic registers one clicky dynamic entity per stored profile, which
// is what puts each profile in the generated CLI: GenerateCLI walks the entity
// registry, so a profile absent from it has no `query profile-<slug>` command.
//
// The registry is a startup snapshot and cannot be otherwise — GenerateCLI has
// already run by the time a profile is created. That is what RegisterFamily is
// for: over HTTP a profile is resolved per request and needs no entry here. The
// two are not alternatives, they serve different consumers.
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
			// A registered filter is what tells the browser which control to render,
			// so every binding registers — including the ones with nothing to
			// enumerate. A range, a date bound and a toggle are typed rather than
			// chosen from, and their empty option set is the accurate answer to
			// "what can this be filtered to": type a value.
			source := entity.FilterSource(entity.StaticOptions(nil))
			if binding.Lookup {
				source = profileFilterSource{service: s, profileName: name, key: binding.Key}
			}
			entity.RegisterFilter(entity.NamedFilter{
				Name:  profileFilterName(resolved.Profile.Name, binding.Column),
				Label: binding.Label, Type: binding.ControlType(), Multi: binding.Multi,
				Limit:  filterLookupLimit(binding),
				Source: source,
			})
		}
		// A bound list param offers the same include/exclude control a column
		// does. profileFilterSource takes an opaque key, so the param's own name
		// routes it through the same lookup without a second source type.
		for _, binding := range resolved.Profile.ParamFilterBindings() {
			filterName := profileParamFilterName(resolved.Profile.Name, binding.Key)
			if _, exists := entity.GetFilter(filterName); exists {
				s.unmarkRegistered(name)
				return fmt.Errorf("profile filter %q is already registered", filterName)
			}
			entity.RegisterFilter(entity.NamedFilter{
				Name: filterName, Label: binding.Label, Type: "multi-filter", Multi: true,
				Limit:  filterLookupLimit(binding),
				Source: profileFilterSource{service: s, profileName: name, key: binding.Key},
			})
		}
		builder := entity.NewDynamicEntity(profileSurfaceKey(name), schemaJSON)
		// A param filters on an input the rows never carry, so it has no schema
		// property to bind through; it is offered by key instead.
		for _, binding := range resolved.Profile.ParamFilterBindings() {
			builder = builder.Filter(binding.Key, profileParamFilterName(resolved.Profile.Name, binding.Key))
		}
		builder.
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
				queryCtx := s.context()
				res, err := query.Execute(queryCtx, live.Profile, toParams(opts))
				if err != nil {
					return nil, err
				}
				// The entity list has no page of its own to report, so the one
				// thing it must not do is present a bounded read as the whole
				// table. `run --all --limit` is the surface that pages.
				if res.Truncated {
					queryCtx.Warnf("profile %q: listed the first %d rows of a larger result; page it with `run --cursor` or raise limits.maxExportRows",
						name, len(res.Rows))
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
	expression := newSampleExpressionHandler(prefix, s.context(), newSampleJSONPathHandler(prefix, next))
	sample := newProfileSampleHandler(prefix, s.context(), expression)
	return newExecHandler(prefix, s.context(), store, sample), nil
}

func (s *Service) OpenAPIHandler(root *cobra.Command, config *rpc.Config) (http.Handler, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	return newProfileOpenAPIHandler(root, config, store, s.openAPIExtensions), nil
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
			// The kind is what decides the control the browser renders, and it
			// is not derivable from the column type alone once a profile can
			// override it.
			prop["x-clicky-filter-kind"] = string(binding.Kind.Normalized())
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
		"x-clicky-icon":   profileIcon(p),
		"x-clicky-path":   profileSurfacePath(p.Name),
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

// profileParamFilterName keys a list param's filter. The -param- segment keeps
// it distinct from the column namespace, so a param and a column that share a
// name do not collide on one registration.
func profileParamFilterName(profileName, paramName string) string {
	digest := sha256.Sum256([]byte(paramName))
	return fmt.Sprintf("profile-%s-param-%x", slugify(profileName), digest[:6])
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
	// This lookup surface takes a plain count; a backend that stated no total is
	// reported as none rather than as zero options behind the ones listed.
	count := 0
	if total != nil {
		count = int(total.Value)
	}
	return result, count, nil
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
