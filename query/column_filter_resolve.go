package query

import (
	stdcontext "context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/inspect"
)

type FilterValueLookupRequest struct {
	Profile    Profile
	Input      map[string]any
	Key        string
	Search     string
	Limit      int
	Inspection InspectionOptions
}

type cachedFilterValues struct {
	Options []FilterOption
	Total   *Total
}

var filterValueCache = inspect.NewMemo(inspect.MemoOptions[cachedFilterValues]{
	Policy: inspect.Policy(inspect.CacheClassFilterValues),
	Weight: func(values cachedFilterValues) int { return len(values.Options) },
})

// resolveProfileInput turns one request's input into the values exposed to the
// query template and the native include/exclude clauses the provider applies.
// Column filters (filter.<column>) and tri-state list params both land in the
// same []ColumnFilterValue, so an exclusion has exactly one transport whichever
// end of the profile declared it. Column filters come first, then params in
// declaration order, so a request builds the same body every time.
//
// now is the clock date-math params resolve against, injected so a paged walk
// can hand every page the instant its first page picked.
func resolveProfileInput(profile Profile, input map[string]any, now time.Time) (map[string]any, []ColumnFilterValue, error) {
	profileParams, filters, err := partitionProfileInput(profile, input)
	if err != nil {
		return nil, nil, err
	}
	resolved, paramFilters, err := resolveParams(profile.Params, profileParams, now)
	if err != nil {
		return nil, nil, err
	}
	return resolved, append(filters, paramFilters...), nil
}

func partitionProfileInput(profile Profile, input map[string]any) (map[string]any, []ColumnFilterValue, error) {
	columns, err := profile.ColumnFilterBindings()
	if err != nil {
		return nil, nil, err
	}
	runtime, err := profile.RuntimeFilterBindings()
	if err != nil {
		return nil, nil, err
	}
	bindings := append(columns, runtime...)
	byKey := make(map[string]ColumnFilterBinding, len(bindings))
	for _, binding := range bindings {
		byKey[binding.Key] = binding
	}
	params := make(map[string]any, len(input))
	values := make(map[string]ColumnFilterValue, len(bindings))
	for key, value := range input {
		binding, isFilter := byKey[key]
		if !isFilter {
			// Only a "filter."-prefixed key claims to be a column filter, so one
			// that matches no binding is a mistake worth naming rather than a
			// param the profile happens not to declare.
			if strings.HasPrefix(key, columnFilterPrefix) {
				return nil, nil, fmt.Errorf("column filter %q is not supported by profile %q", key, profile.Name)
			}
			params[key] = value
			continue
		}
		selection, err := binding.resolveSelection(value)
		if err != nil {
			return nil, nil, fmt.Errorf("column filter %q: %w", key, err)
		}
		if selection.IsZero() {
			continue
		}
		values[key] = selection
	}
	filters := make([]ColumnFilterValue, 0, len(values))
	for _, binding := range bindings {
		value, ok := values[binding.Key]
		if !ok && binding.Default != "" {
			// A default stands in for the request that named nothing, so it is
			// read under the binding's own grammar rather than trusted as a
			// pre-parsed value — a default that cannot be selected by hand is a
			// default nobody could have reproduced.
			defaulted, err := binding.resolveSelection(binding.Default)
			if err != nil {
				return nil, nil, fmt.Errorf("column filter %q default: %w", binding.Key, err)
			}
			value, ok = defaulted, !defaulted.IsZero()
		}
		if ok {
			filters = append(filters, value)
		}
	}
	return params, filters, nil
}

// resolveSelection parses one request value and stamps the binding's identity
// onto it, which is what every consumer downstream reads it by.
func (b ColumnFilterBinding) resolveSelection(value any) (ColumnFilterValue, error) {
	selection, err := b.parseSelection(value)
	if err != nil {
		return ColumnFilterValue{}, err
	}
	selection.Column, selection.Key, selection.Field = b.Column, b.Key, b.Field
	selection.Nested, selection.Where = b.Nested, b.Where
	return selection, nil
}

func LookupFilterValues(ctx context.Context, request FilterValueLookupRequest) ([]FilterOption, *Total, error) {
	profile, input := request.Profile, request.Input
	key, search, limit := request.Key, request.Search, request.Limit
	if err := profile.Validate(); err != nil {
		return nil, nil, err
	}
	if profile.Namespace != "" {
		ctx = ctx.WithNamespace(profile.Namespace)
	}
	resolved, filters, err := resolveProfileInput(profile, input, time.Now())
	if err != nil {
		return nil, nil, fmt.Errorf("profile %q: %w", profile.Name, err)
	}
	binding, err := profile.filterBinding(key)
	if err != nil {
		return nil, nil, err
	}
	if !binding.Kind.Lookupable() {
		return nil, nil, fmt.Errorf("filter %q is a %s filter and has no values to list", key, binding.Kind.Normalized())
	}
	// A declared limit narrows what the caller asked for; it never widens it.
	// The guard is on the binding rather than the caller because a binding
	// nobody declared — every inferred one, and every one the connection browser
	// builds — must keep answering the size its caller chose.
	if binding.Limit > 0 && (limit <= 0 || binding.Limit < limit) {
		limit = binding.Limit
	}
	// An enumerated filter already carries the answer, so asking the backend
	// would be a round trip whose result is sitting in the profile. It is
	// deliberately served whole: `options` names the values that exist, so
	// withholding some of them would answer a different question than the one
	// the author wrote.
	if len(binding.Options) > 0 {
		options := make([]FilterOption, 0, len(binding.Options))
		for _, option := range binding.Options {
			if search == "" || strings.Contains(strings.ToLower(option), strings.ToLower(search)) {
				options = append(options, FilterOption{Value: option})
			}
		}
		// An enumerated filter is the whole set by construction, so the count is
		// the number and not an estimate of it.
		return options, &Total{Value: int64(len(options)), Exact: true}, nil
	}
	// The filter being looked up must not narrow its own options, or a chosen
	// value would hide every alternative. Every other active selection — column
	// or param — still scopes the question.
	siblings := make([]ColumnFilterValue, 0, len(filters))
	for _, filter := range filters {
		if filter.Key != key {
			siblings = append(siblings, filter)
		}
	}
	provider, err := GetProvider(profile.Provider.Type)
	if err != nil {
		return nil, nil, err
	}
	lookup, ok := provider.(FilterLookupProvider)
	if !ok {
		return nil, nil, fmt.Errorf("provider %q does not support column filter lookups", profile.Provider.Type)
	}
	req, err := buildProviderRequest(ctx, profile.Provider, profile.Query, profile.Params, resolved)
	if err != nil {
		return nil, nil, fmt.Errorf("profile %q: %w", profile.Name, err)
	}
	req.Filters = siblings
	req.Inspection = request.Inspection
	identity, err := ctx.ConnectionCacheIdentity(req.Connection)
	if err != nil {
		return nil, nil, fmt.Errorf("filter %q connection identity: %w", key, err)
	}
	cacheKey, err := filterValueCacheKey(identity, filterLookupProviderIdentity(lookup), req, binding, search, limit)
	if err != nil {
		return nil, nil, err
	}
	result, err := filterValueCache.Get(ctx, inspect.GetOptions[cachedFilterValues]{
		Key: cacheKey, Refresh: request.Inspection.Refresh,
		Load: func(fillContext stdcontext.Context) (cachedFilterValues, error) {
			lookupContext, lookupRequest, operation, err := prepareConnectionOperation(ctx.Wrap(fillContext), req)
			if err != nil {
				return cachedFilterValues{}, err
			}
			options, total, err := lookup.LookupFilterValues(lookupContext, lookupRequest, binding, search, limit)
			operation.Finish(len(options), err)
			return cachedFilterValues{Options: options, Total: total}, err
		},
	})
	if err != nil && !result.Cache.Cached {
		return nil, nil, err
	}
	return result.Value.Options, result.Value.Total, nil
}

func filterValueCacheKey(
	identity string,
	providerIdentity string,
	req ProviderRequest,
	binding ColumnFilterBinding,
	search string,
	limit int,
) (string, error) {
	payload, err := json.Marshal(struct {
		Identity         string
		ProviderIdentity string
		Provider         string
		Query            string
		Options          map[string]any
		Params           map[string]any
		ParamRoles       map[string]ParamRole
		TemplatedParams  []string
		Filters          []ColumnFilterValue
		Binding          ColumnFilterBinding
		Search           string
		Limit            int
	}{
		Identity: identity, ProviderIdentity: providerIdentity,
		Provider: req.Provider, Query: req.Query,
		Options: req.Options, Params: req.Params, ParamRoles: req.ParamRoles,
		TemplatedParams: req.TemplatedParams, Filters: req.Filters,
		Binding: binding, Search: search, Limit: limit,
	})
	if err != nil {
		return "", fmt.Errorf("encode filter value cache key: %w", err)
	}
	return fmt.Sprintf("filter-values:%x", sha256.Sum256(payload)), nil
}

func filterLookupProviderIdentity(provider FilterLookupProvider) string {
	value := reflect.ValueOf(provider)
	if value.Kind() == reflect.Pointer && !value.IsNil() {
		return fmt.Sprintf("%T:%x", provider, value.Pointer())
	}
	return fmt.Sprintf("%T:%#v", provider, provider)
}

// ResolveColumnFilters resolves filter.<column> request values against a
// profile's columns, for a caller that assembled the profile itself rather than
// loading a stored one — the connection browser, which infers its columns from
// the rows a first, unfiltered run returned. Params are not resolved here: an
// assembled profile declares none.
func ResolveColumnFilters(profile Profile, input map[string]any) ([]ColumnFilterValue, error) {
	_, filters, err := partitionProfileInput(profile, input)
	if err != nil {
		return nil, err
	}
	return filters, nil
}
