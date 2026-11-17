package profiles

import (
	"maps"
	"slices"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/projections"
)

// RunProfile is the projections.ProfileRunner of a profile service: the same read
// as `query profiles run <name> [--all] --param key=value…`.
func (s *Service) RunProfile(ctx dbcontext.Context, request projections.ProfileRunRequest) (projections.ProfileRunResult, error) {
	params := make([]string, 0, len(request.Params))
	for _, key := range slices.Sorted(maps.Keys(request.Params)) {
		params = append(params, key+"="+request.Params[key])
	}
	result, err := s.Run(ctx, request.Name, RunFlags{All: request.All, Params: params})
	if err != nil {
		return projections.ProfileRunResult{}, err
	}
	return projections.ProfileRunResult{
		Rows: result.Rows, Truncated: result.Truncated, HasMore: result.HasMore, Metrics: result.Metrics,
	}, nil
}
