package profiles

import (
	"context"
	"fmt"
	"strings"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/commons-db/query"
)

// profileFamilyName is the path segment every profile is served under:
// {prefix}/profile/{name}.
const profileFamilyName = "profile"

// profileSurfaceKey is the identity a profile carries everywhere outside the
// store — the OpenAPI path's last segment, the x-clicky surface key, and the
// frontend route. findProfile maps it back to the stored name.
func profileSurfaceKey(name string) string { return "profile-" + slugify(name) }

// RegisterFamily routes every profile — including one created a moment ago — at
// {prefix}/profile/{name}.
//
// The entity -> Cobra -> RPC -> mux pipeline is one snapshot taken at startup, so
// a profile that comes into being while the server runs has no route and no way
// to acquire one. That is why x-clicky-lookup.url pointed at
// {prefix}/profile/profile-<slug> and nothing answered there: the request fell
// through the whole middleware chain to a mux with no such pattern. A family
// registers one route for the shape and resolves the instance in front of every
// request, so nothing has to be re-registered or invalidated when a profile is
// saved.
//
// Paged is deliberately nil, so execHandler keeps serving the rows. clicky's
// paged transport refuses the clicky-json envelope the interactive table asks
// for, bounds pages by the library's limits rather than the profile's, and
// cannot turn a PDF over its ceiling into a status the caller can read. What it
// does own — resolving ?__lookup=filters from a spec that did not exist at
// startup — is exactly what was missing, and execHandler already delegates that
// request rather than answering it.
func (s *Service) RegisterFamily() {
	entity.RegisterDynamicEntityFamily(entity.DynamicEntityFamily{
		Name:    profileFamilyName,
		Parent:  profileSurfaceParent,
		Resolve: s.resolveSurface,
		List:    s.listSurfaces,
	})
}

// resolveSurface describes one profile as it exists right now. It runs in front
// of every request to the family, so it reads the store rather than a snapshot.
func (s *Service) resolveSurface(ctx context.Context, name string) (entity.DynamicEntitySpec, error) {
	store, err := s.store()
	if err != nil {
		return entity.DynamicEntitySpec{}, err
	}
	resolved, err := Resolve(ctx, store, name)
	if err != nil {
		return entity.DynamicEntitySpec{}, entity.UnknownDynamicEntity(profileFamilyName, name)
	}
	return s.profileSurface(resolved.Profile)
}

// listSurfaces enumerates the profiles that exist as the OpenAPI document is
// read, for the same reason resolveSurface reads the store: a document rendered
// once at startup would describe a set that has since changed.
func (s *Service) listSurfaces(ctx context.Context) ([]entity.DynamicEntitySpec, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	stored, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	surfaces := make([]entity.DynamicEntitySpec, 0, len(stored))
	for _, profile := range stored {
		resolved, err := ResolveWithoutTouch(ctx, store, profile.Name)
		if err != nil {
			return nil, fmt.Errorf("resolve profile surface %q: %w", profile.Name, err)
		}
		surface, err := s.profileSurface(resolved.Profile)
		if err != nil {
			return nil, err
		}
		surfaces = append(surfaces, surface)
	}
	return surfaces, nil
}

// profileSurface is a resolved profile as a clicky entity: the metadata the
// sidebar renders from, and one filter per filterable column or bound list
// param.
//
// Name is the surface key rather than the profile name because clicky derives
// both the route's last segment and x-clicky.surfaces[].key from that one field,
// and the frontend already routes on profile-<slug>.
func (s *Service) profileSurface(p query.Profile) (entity.DynamicEntitySpec, error) {
	filters, err := s.profileFilters(p)
	if err != nil {
		return entity.DynamicEntitySpec{}, err
	}
	name := p.Name
	return entity.DynamicEntitySpec{
		Name:    profileSurfaceKey(p.Name),
		Parent:  profileSurfaceParent,
		Icon:    profileIcon(p),
		Path:    profileSurfacePath(p.Name),
		Title:   p.Name,
		Filters: filters,
		List: func(ctx context.Context, flags map[string]string, _ []string) (any, error) {
			return s.executeRows(ctx, name, flags)
		},
	}, nil
}

// profileFilters is one control per filterable column and per bound list param.
//
// Every binding produces a filter, including the ones with nothing to enumerate:
// the filter is what tells the browser which control to render, and a range, a
// date bound and a toggle are typed rather than chosen from — an empty option
// set is the accurate answer to "what can this be filtered to".
func (s *Service) profileFilters(p query.Profile) ([]entity.DynamicFilter, error) {
	bindings, err := p.FilterBindings()
	if err != nil {
		return nil, fmt.Errorf("build filters for profile %q: %w", p.Name, err)
	}
	filters := make([]entity.DynamicFilter, 0, len(bindings))
	for _, binding := range bindings {
		filters = append(filters, s.profileFilter(p.Name, binding))
	}
	return append(filters, timeRangeParamFilters(p)...), nil
}

// timeRangeParamFilters describes the two edges of a profile-declared time
// bound. They enumerate nothing — the browser already pairs the parameters by
// their roles — but an undescribed edge is read as a whole-day one, which would
// take the clock off a window the parameter resolves to the nanosecond.
//
// The clock follows the declared type rather than the role: a date parameter is
// truncated to whole days when it resolves (query.ParamDef.coerce), so offering
// an hour there would offer a precision the value cannot carry.
func timeRangeParamFilters(p query.Profile) []entity.DynamicFilter {
	types := []struct {
		role    query.ParamRole
		control string
	}{
		{role: query.ParamRoleTimeFrom, control: "from"},
		{role: query.ParamRoleTimeTo, control: "to"},
	}
	declared := p.TimeRangeParams()
	filters := make([]entity.DynamicFilter, 0, len(types))
	for _, edge := range types {
		param, ok := declared[edge.role]
		if !ok {
			continue
		}
		timeEnabled := param.Type == query.ParamTypeDateTime
		filters = append(filters, entity.DynamicFilter{
			Key:         param.Name,
			Label:       param.DisplayLabel(),
			Type:        edge.control,
			TimeEnabled: &timeEnabled,
			Options: func(context.Context, map[string]string, string, int) (map[string]api.Textable, int, error) {
				return nil, 0, nil
			},
		})
	}
	return filters
}

func (s *Service) profileFilter(profileName string, binding query.ColumnFilterBinding) entity.DynamicFilter {
	source := s.bindingSource(profileName, binding)
	// Every source call carries the request's sibling filter values, so an option
	// set can narrow against what is already selected.
	filterContext := func(ctx context.Context, flags map[string]string) entity.FilterContext {
		return entity.FilterContext{Context: ctx, Key: binding.Key, Params: flags}
	}
	return entity.DynamicFilter{
		Key:        binding.Key,
		Label:      binding.Label,
		Type:       binding.ControlType(),
		Multi:      binding.Multi,
		Searchable: binding.Lookup,
		Limit:      filterLookupLimit(binding),
		Options: func(ctx context.Context, flags map[string]string, search string, limit int) (map[string]api.Textable, int, error) {
			return source.Options(filterContext(ctx, flags), search, limit)
		},
		Selected: func(ctx context.Context, flags map[string]string) (map[string]api.Textable, error) {
			values := selectedFilterValues(flags[binding.Key])
			if len(values) == 0 {
				return nil, nil
			}
			return source.Resolve(filterContext(ctx, flags), values)
		},
	}
}

// filterLookupLimit is how many values this filter offers before the rest have
// to be typed for.
//
// The default lives here rather than on the binding because a binding that
// declared nothing means "whoever asks decides" — which is what lets the
// connection browser keep choosing its own size — and a profile surface is the
// one asking here.
func filterLookupLimit(binding query.ColumnFilterBinding) int {
	if binding.Limit > 0 {
		return binding.Limit
	}
	return query.DefaultFilterLookupLimit
}

// bindingSource decides where a binding's options come from: the backend for a
// value selection, the profile itself for an enumerated one, and nowhere for a
// control that is typed rather than picked. Asking the backend to enumerate a
// set the profile already lists would be a round trip whose answer is sitting in
// the binding.
func (s *Service) bindingSource(profileName string, binding query.ColumnFilterBinding) entity.FilterSource {
	if binding.Lookup {
		return profileFilterSource{service: s, profileName: profileName, key: binding.Key}
	}
	if len(binding.Options) == 0 {
		return entity.StaticOptions(nil)
	}
	options := make(map[string]api.Textable, len(binding.Options))
	for _, option := range binding.Options {
		options[option] = api.Text{Content: option}
	}
	return entity.StaticOptions(options)
}

// selectedFilterValues reads the values a request already carries for a filter,
// so the control can label what is currently chosen. A leading "!" is an exclude
// marker on the selection, not part of the value it excludes.
func selectedFilterValues(raw string) []string {
	values := make([]string, 0, strings.Count(raw, ",")+1)
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimPrefix(strings.TrimSpace(value), "!"); value != "" {
			values = append(values, value)
		}
	}
	return values
}

// executeRows runs a profile and returns its rows, re-resolving it first so an
// edit made since the surface was described is what actually runs.
func (s *Service) executeRows(ctx context.Context, name string, opts map[string]string) ([]map[string]any, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	live, err := Resolve(ctx, store, name)
	if err != nil {
		return nil, err
	}
	// The base profile flow needs no database; only postgres/sqlite processors
	// do. The context provider supplies the DB-backed context under `serve` and a
	// DB-less one on the CLI.
	queryCtx := s.context()
	res, err := query.Execute(queryCtx, live.Profile, toParams(opts))
	if err != nil {
		return nil, err
	}
	// This list has no page of its own to report, so the one thing it must not do
	// is present a bounded read as the whole table. `run --all --limit` is the
	// surface that pages.
	if res.Truncated {
		queryCtx.Warnf("profile %q: listed the first %d rows of a larger result; page it with `run --cursor` or raise limits.maxExportRows",
			name, len(res.Rows))
	}
	return res.Rows, nil
}
