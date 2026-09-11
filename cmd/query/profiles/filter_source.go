package profiles

import (
	"strings"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/entity"
	"github.com/flanksource/commons-db/query"
)

// profileFilterSource answers one bound filter's lookup from the profile's
// backend, through the query engine the execution path uses.
type profileFilterSource struct {
	service     *Service
	profileName string
	key         string
}

func (source profileFilterSource) Options(fc entity.FilterContext, search string, limit int) (map[string]api.Textable, int, error) {
	result, err := source.CountedOptions(fc, search, limit)
	return result.Options, result.Total, err
}

func (source profileFilterSource) CountedOptions(fc entity.FilterContext, search string, limit int) (entity.FilterOptions, error) {
	store, err := source.service.store()
	if err != nil {
		return entity.FilterOptions{}, err
	}
	resolved, err := Resolve(fc.Ctx(), store, source.profileName)
	if err != nil {
		return entity.FilterOptions{}, err
	}
	input := filterLookupParams(resolved.Profile, fc.Params)
	release, err := source.service.prepareRead(fc.Ctx(), resolved.Profile, input)
	if err != nil {
		return entity.FilterOptions{}, prepareStatusError(err)
	}
	defer release()

	options, total, err := query.LookupFilterValues(
		source.service.context().Wrap(fc.Ctx()),
		query.FilterValueLookupRequest{
			Profile: resolved.Profile, Input: input,
			Key: source.key, Search: search, Limit: limit,
		},
	)
	if err != nil {
		return entity.FilterOptions{}, err
	}
	return toFilterOptions(options, total), nil
}

// toFilterOptions is the engine's answer in the lookup's shape. A value the
// backend listed holds at least one row, so a zero count is one the backend did
// not take — an enumerated option, say — and is left out rather than reported
// as an empty value; a backend that counted nothing yields no counts at all.
func toFilterOptions(options []query.FilterOption, total *query.Total) entity.FilterOptions {
	result := entity.FilterOptions{Options: make(map[string]api.Textable, len(options))}
	for _, option := range options {
		result.Options[option.Value] = api.Text{Content: option.Value}
		if option.Count <= 0 {
			continue
		}
		if result.Counts == nil {
			result.Counts = make(map[string]int, len(options))
		}
		result.Counts[option.Value] = int(option.Count)
	}
	// This lookup surface takes a plain count; a backend that stated no total is
	// reported as none rather than as zero options behind the ones listed.
	if total != nil {
		result.Total = int(total.Value)
	}
	return result
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
