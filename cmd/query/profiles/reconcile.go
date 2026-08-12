package profiles

import (
	"context"
	"fmt"

	"github.com/flanksource/commons-db/query"
)

// ReconcileFlags are the flags of the profiles `reconcile` action. The entity id
// is the source profile; every flag overrides the corresponding field of the
// reconcile the source profile stores, so a saved join runs with no flags at all
// and an ad-hoc one supplies its own.
type ReconcileFlags struct {
	Dest       string   `flag:"dest" help:"Profile to reconcile against; required unless the source profile stores a reconcile block"`
	KeyCEL     string   `flag:"key-cel" help:"CEL expression evaluated against a row on either side to derive the join key"`
	KeyColumns []string `flag:"key-columns" help:"Column names whose values form the join key (use --key-cel when the two sides name them differently)"`
	TimeColumn string   `flag:"time-column" help:"Row key holding each side's event time; defaults to the profile's timestamp column"`
	KeyFrom    string   `flag:"key-from" help:"Reconcile keys at or after this one; empty starts at the first key"`
	KeyTo      string   `flag:"key-to" help:"Reconcile keys before this one; empty runs to the last key"`
	Params     []string `flag:"param" help:"Profile filter param as key=value (repeatable), applied to whichever side declares it"`
	Outcome    string   `flag:"outcome" help:"Return one result outcome: matched, only_source, only_dest, or ambiguous"`
}

func (ReconcileFlags) ClickyActionFlags() {}

// Reconcile runs two profiles and joins their results on a shared key,
// reporting which records made it across and how long they took.
func (s *Service) Reconcile(ctx context.Context, name string, options ReconcileFlags) (*query.ReconcileResult, error) {
	if err := validateReconcileOutcome(options.Outcome); err != nil {
		return nil, err
	}
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	source, err := Resolve(ctx, store, name)
	if err != nil {
		return nil, err
	}

	// parseParamValues rather than parseKeyValues: it is the one that refuses an
	// @file reference, which an action served over HTTP must.
	parsed, err := parseParamValues(options.Params)
	if err != nil {
		return nil, err
	}
	flagParams := make(map[string]string, len(parsed))
	for key, value := range parsed {
		flagParams[key] = fmt.Sprint(value)
	}
	config, err := reconcileConfig(source.Profile, options, flagParams)
	if err != nil {
		return nil, err
	}

	dest, err := Resolve(ctx, store, config.Dest)
	if err != nil {
		return nil, err
	}

	params := make(map[string]any, len(config.Params))
	for key, value := range config.Params {
		params[key] = value
	}

	queryCtx := s.context().Wrap(ctx)
	result, err := query.ReconcileProfiles(queryCtx, query.ReconcileRun{
		Source:       source.Profile,
		Dest:         dest.Profile,
		Config:       config,
		SourceParams: paramsFor(queryCtx, source.Profile, params, "source"),
		DestParams:   paramsFor(queryCtx, dest.Profile, params, "dest"),
	})
	if err != nil {
		return nil, err
	}
	if err := selectReconcileOutcome(result, options.Outcome); err != nil {
		return nil, err
	}
	return result, nil
}

func selectReconcileOutcome(result *query.ReconcileResult, outcome string) error {
	if err := validateReconcileOutcome(outcome); err != nil {
		return err
	}
	if outcome == "" {
		return nil
	}

	rows := make([]query.ReconcileRow, 0, len(result.Rows))
	stats := query.ReconcileStats{}
	seen := make(map[string]struct{})
	for _, row := range result.Rows {
		duplicated := row.SourceDupCount > 1 || row.DestDupCount > 1
		if (outcome == "ambiguous" && !duplicated) || (outcome != "ambiguous" && outcome != string(row.Status)) {
			continue
		}
		rows = append(rows, row)
		if _, ok := seen[row.Key]; ok {
			continue
		}
		seen[row.Key] = struct{}{}
		switch row.Status {
		case query.ReconcileMatched:
			stats.Matched++
		case query.ReconcileOnlySource:
			stats.OnlySource++
		case query.ReconcileOnlyDest:
			stats.OnlyDest++
		}
		if duplicated {
			stats.DupKeys++
		}
	}
	result.Rows = rows
	result.Stats = stats
	return nil
}

func validateReconcileOutcome(outcome string) error {
	if outcome != "" &&
		outcome != string(query.ReconcileMatched) &&
		outcome != string(query.ReconcileOnlySource) &&
		outcome != string(query.ReconcileOnlyDest) &&
		outcome != "ambiguous" {
		return fmt.Errorf("invalid reconcile outcome %q: expected matched, only_source, only_dest, or ambiguous", outcome)
	}
	return nil
}

// reconcileConfig merges the reconcile stored on the source profile with the
// flags of this invocation, field by field: a flag that was given wins, one that
// was not leaves the stored value alone. Params merge per key rather than
// replacing the stored set, so overriding one filter does not silently drop the
// rest.
func reconcileConfig(source query.Profile, options ReconcileFlags, flagParams map[string]string) (query.ReconcileConfig, error) {
	config := query.ReconcileConfig{}
	if source.Reconcile != nil {
		config = *source.Reconcile
	}
	if options.Dest != "" {
		config.Dest = options.Dest
	}
	if options.KeyCEL != "" {
		config.Key = query.KeySpec{CEL: options.KeyCEL}
	}
	if len(options.KeyColumns) > 0 {
		config.Key = query.KeySpec{Columns: options.KeyColumns}
	}
	if options.KeyCEL != "" && len(options.KeyColumns) > 0 {
		return config, fmt.Errorf("--key-cel and --key-columns both set; pick one")
	}
	if options.TimeColumn != "" {
		config.TimeColumn = options.TimeColumn
	}
	// A range narrows both sides to the same keys, which a per-side row cap
	// could not: two sides cut at N rows each are two different key sets, so
	// the bound itself produced the one-sided keys it then reported.
	if options.KeyFrom != "" || options.KeyTo != "" {
		config.Range = &query.KeyRange{From: options.KeyFrom, To: options.KeyTo}
	}
	if len(flagParams) > 0 {
		merged := make(map[string]string, len(config.Params)+len(flagParams))
		for key, value := range config.Params {
			merged[key] = value
		}
		for key, value := range flagParams {
			merged[key] = value
		}
		config.Params = merged
	}
	if config.Dest == "" {
		return config, fmt.Errorf("--dest is required: reconcile joins two profiles, and %q stores no reconcile block", source.Name)
	}
	if err := config.Range.Validate(); err != nil {
		return config, err
	}
	return config, nil
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
