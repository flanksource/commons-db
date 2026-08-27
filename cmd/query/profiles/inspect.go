package profiles

import (
	"context"

	"github.com/flanksource/commons-db/query"
)

type InspectFlags struct {
	Params  []string `flag:"param" help:"Profile parameter as key=value (repeatable)"`
	Refresh bool     `flag:"refresh" help:"Refresh cached inspection metadata"`
}

func (InspectFlags) ClickyActionFlags() {}

func (s *Service) Inspect(ctx context.Context, name string, options InspectFlags) (*query.InspectionResult, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	resolved, err := Resolve(ctx, store, name)
	if err != nil {
		return nil, err
	}
	params, err := parseParamValues(options.Params)
	if err != nil {
		return nil, err
	}
	sample, err := query.Sample(s.context().Wrap(ctx), resolved.Profile, query.SampleOptions{
		Params: params, Inspection: query.InspectionOptions{Refresh: options.Refresh},
	})
	if err != nil {
		return nil, err
	}
	return query.NewProfileInspectionResult(resolved.Profile, sample), nil
}
