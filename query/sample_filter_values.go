package query

import (
	"fmt"
	"time"

	"github.com/flanksource/commons-db/context"
)

type SampleFilterValuesOptions struct {
	Params        map[string]any
	Filters       map[string]string
	FilterColumns []ColumnDef
	FilterKey     string
	Search        string
	Limit         int
	Inspection    InspectionOptions
}

// SampleFilterValues lists one draft profile filter's values without weakening
// the read-only contract used by Sample.
func SampleFilterValues(
	ctx context.Context,
	profile Profile,
	options SampleFilterValuesOptions,
) ([]FilterOption, *Total, error) {
	if err := profile.Validate(); err != nil {
		return nil, nil, err
	}
	if profile.Kind() != KindQuery {
		return nil, nil, fmt.Errorf("profile %q is not a single query and cannot be sampled", profile.Name)
	}
	if profile.Namespace != "" {
		ctx = ctx.WithNamespace(profile.Namespace)
	}
	input, err := sampleInput(options.Params, options.Filters)
	if err != nil {
		return nil, nil, fmt.Errorf("profile %q: %w", profile.Name, err)
	}
	filterProfile := sampleFilterProfile(profile, options.FilterColumns)
	resolved, _, err := resolveProfileInput(filterProfile, input, time.Now())
	if err != nil {
		return nil, nil, fmt.Errorf("profile %q: %w", profile.Name, err)
	}
	request, err := buildProviderRequest(ctx, profile.Provider, profile.Query, profile.Params, resolved)
	if err != nil {
		return nil, nil, fmt.Errorf("profile %q: %w", profile.Name, err)
	}
	if err := validateSampleReadOnly(profile.Provider.Type, request.Query, request.Options); err != nil {
		return nil, nil, fmt.Errorf("profile %q: %w", profile.Name, err)
	}
	return LookupFilterValues(ctx, FilterValueLookupRequest{
		Profile: filterProfile, Input: input, Key: options.FilterKey,
		Search: options.Search, Limit: options.Limit, Inspection: options.Inspection,
	})
}
