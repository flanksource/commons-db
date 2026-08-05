package profiles

import (
	"context"
	"fmt"

	"github.com/flanksource/commons-db/query"
)

// ReconcileFlags are the flags of the profiles `reconcile` action. The entity id
// is the source profile; --dest names the other side.
type ReconcileFlags struct {
	Dest       string   `flag:"dest" help:"Profile to reconcile against" required:"true"`
	KeyCEL     string   `flag:"key-cel" help:"CEL expression evaluated against a row on either side to derive the join key"`
	KeyColumns []string `flag:"key-columns" help:"Column names whose values form the join key (use --key-cel when the two sides name them differently)"`
	TimeColumn string   `flag:"time-column" help:"Row key holding each side's event time; defaults to the profile's timestamp column"`
	Params     []string `flag:"param" help:"Profile filter param as key=value (repeatable), applied to whichever side declares it"`
}

func (ReconcileFlags) ClickyActionFlags() {}

// Reconcile runs two profiles and joins their results on a shared key,
// reporting which records made it across and how long they took.
func (s *Service) Reconcile(ctx context.Context, name string, options ReconcileFlags) (*query.ReconcileResult, error) {
	if options.Dest == "" {
		return nil, fmt.Errorf("--dest is required: reconcile joins two profiles")
	}
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	source, err := Resolve(ctx, store, name)
	if err != nil {
		return nil, err
	}
	dest, err := Resolve(ctx, store, options.Dest)
	if err != nil {
		return nil, err
	}

	params, err := parseParamValues(options.Params)
	if err != nil {
		return nil, err
	}

	queryCtx := s.context().Wrap(ctx)
	sourceResult, err := query.Execute(queryCtx, source.Profile, paramsFor(queryCtx, source.Profile, params, "source"))
	if err != nil {
		return nil, fmt.Errorf("source profile %q: %w", name, err)
	}
	destResult, err := query.Execute(queryCtx, dest.Profile, paramsFor(queryCtx, dest.Profile, params, "dest"))
	if err != nil {
		return nil, fmt.Errorf("dest profile %q: %w", options.Dest, err)
	}

	return query.Reconcile(queryCtx, sourceResult, destResult, source.Profile, dest.Profile, query.ReconcileSpec{
		Key:        query.KeySpec{Columns: options.KeyColumns, CEL: options.KeyCEL},
		TimeColumn: options.TimeColumn,
	})
}

// paramsFor narrows the caller's filters to those the given profile declares.
// The two sides rarely accept the same filter set, and refusing the whole run
// because one side lacks a filter would make reconcile unusable across
// heterogeneous backends — so an unsupported filter is dropped with a debug log
// rather than raised.
func paramsFor(ctx interface{ Debugf(string, ...any) }, profile query.Profile, params map[string]any, side string) map[string]any {
	declared := make(map[string]struct{}, len(profile.Params))
	for _, param := range profile.Params {
		declared[param.Name] = struct{}{}
	}
	out := make(map[string]any, len(params))
	for key, value := range params {
		if _, ok := declared[key]; !ok {
			ctx.Debugf("reconcile: %s profile %q does not declare param %q; dropping", side, profile.Name, key)
			continue
		}
		out[key] = value
	}
	return out
}
