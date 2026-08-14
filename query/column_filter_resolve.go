package query

import (
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/context"
)

// resolveProfileInput turns one request's input into the values exposed to the
// query template and the native include/exclude clauses the provider applies.
// Column filters (filter.<column>) and tri-state list params both land in the
// same []ColumnFilterValue, so an exclusion has exactly one transport whichever
// end of the profile declared it. Column filters come first, then params in
// declaration order, so a request builds the same body every time.
func resolveProfileInput(profile Profile, input map[string]any) (map[string]any, []ColumnFilterValue, error) {
	profileParams, filters, err := partitionProfileInput(profile, input)
	if err != nil {
		return nil, nil, err
	}
	resolved, paramFilters, err := resolveParams(profile.Params, profileParams)
	if err != nil {
		return nil, nil, err
	}
	return resolved, append(filters, paramFilters...), nil
}

func partitionProfileInput(profile Profile, input map[string]any) (map[string]any, []ColumnFilterValue, error) {
	bindings, err := profile.ColumnFilterBindings()
	if err != nil {
		return nil, nil, err
	}
	byKey := make(map[string]ColumnFilterBinding, len(bindings))
	for _, binding := range bindings {
		byKey[binding.Key] = binding
	}
	params := make(map[string]any, len(input))
	values := make(map[string]ColumnFilterValue, len(bindings))
	for key, value := range input {
		if !strings.HasPrefix(key, columnFilterPrefix) {
			params[key] = value
			continue
		}
		binding, ok := byKey[key]
		if !ok {
			return nil, nil, fmt.Errorf("column filter %q is not supported by profile %q", key, profile.Name)
		}
		selection, err := binding.parseSelection(value)
		if err != nil {
			return nil, nil, fmt.Errorf("column filter %q: %w", key, err)
		}
		if selection.IsZero() {
			continue
		}
		selection.Column, selection.Key, selection.Field = binding.Column, binding.Key, binding.Field
		selection.Nested, selection.Where = binding.Nested, binding.Where
		values[key] = selection
	}
	filters := make([]ColumnFilterValue, 0, len(values))
	for _, binding := range bindings {
		if value, ok := values[binding.Key]; ok {
			filters = append(filters, value)
		}
	}
	return params, filters, nil
}

func LookupFilterValues(ctx context.Context, profile Profile, input map[string]any, key, search string, limit int) ([]FilterOption, *Total, error) {
	if err := profile.Validate(); err != nil {
		return nil, nil, err
	}
	if profile.Namespace != "" {
		ctx = ctx.WithNamespace(profile.Namespace)
	}
	resolved, filters, err := resolveProfileInput(profile, input)
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
	return lookup.LookupFilterValues(ctx, req, binding, search, limit)
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
