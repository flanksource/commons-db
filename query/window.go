package query

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"
)

// ErrUnwindowed is a read that a window cannot bound: its provider declares no
// range, and its query, connection and options reference neither params.from
// nor params.to, so it would read everything whatever window it is given.
var ErrUnwindowed = errors.New("the read cannot be bounded by a window")

// ReadWindow is the span of time a profile is read over. Step, when set,
// replaces a provider range's declared step; zero keeps it.
type ReadWindow struct {
	Start, End time.Time
	Step       time.Duration
}

// windowLayout spells a window bound to the millisecond, which both date math
// and SQL date functions read.
const windowLayout = "2006-01-02T15:04:05.000Z07:00"

// WindowProfile binds window onto a read of p with params, returning both
// copied, never the caller's.
//
// A provider range option is replaced by the window. A provider without one
// gets the window as params.from and params.to, declared when p references
// them undeclared, and as every param declaring the time-from or time-to
// role; p must reference one of them, or the read is refused with
// ErrUnwindowed rather than read unbounded.
func WindowProfile(p Profile, params map[string]any, window ReadWindow) (Profile, map[string]any, error) {
	params = maps.Clone(params)
	if params == nil {
		params = map[string]any{}
	}
	start, end := window.Start.UTC().Format(windowLayout), window.End.UTC().Format(windowLayout)
	if declared, ok := p.Provider.Options["range"]; ok {
		step, err := windowStep(p.Name, declared, window.Step)
		if err != nil {
			return Profile{}, nil, err
		}
		p.Provider.Options = maps.Clone(p.Provider.Options)
		p.Provider.Options["range"] = map[string]any{"start": start, "end": end, "step": step}
		return p, params, nil
	}
	reads, err := windowParamsRead(p)
	if err != nil {
		return Profile{}, nil, err
	}
	if len(reads) == 0 {
		return Profile{}, nil, fmt.Errorf("%w: profile %q declares no provider range and references neither params.from nor params.to", ErrUnwindowed, p.Name)
	}
	bounds := map[string]string{"from": start, "to": end, string(ParamRoleTimeFrom): start, string(ParamRoleTimeTo): end}
	p.Params = slices.Clone(p.Params)
	for _, name := range []string{"from", "to"} {
		if !slices.Contains(reads, name) {
			continue
		}
		if !slices.ContainsFunc(p.Params, func(def ParamDef) bool { return def.Name == name }) {
			p.Params = append(p.Params, ParamDef{Name: name})
		}
		params[name] = bounds[name]
	}
	for _, def := range p.Params {
		if bound, ok := bounds[string(def.Role)]; ok {
			params[def.Name] = bound
		}
	}
	return p, params, nil
}

// windowStep is the step a windowed range reads at: the window's, else the
// one the range declares.
func windowStep(profile string, declared any, step time.Duration) (string, error) {
	if step > 0 {
		return step.String(), nil
	}
	if declaredRange, ok := declared.(map[string]any); ok {
		if text, ok := declaredRange["step"].(string); ok && text != "" {
			return text, nil
		}
	}
	return "", fmt.Errorf("profile %q range declares no step, and the window names none", profile)
}

// windowParamsRead are the window params p reads: from and to where it
// declares or references them, and every time-role param it declares.
func windowParamsRead(p Profile) ([]string, error) {
	options, err := json.Marshal(p.Provider.Options)
	if err != nil {
		return nil, fmt.Errorf("profile %q options: %w", p.Name, err)
	}
	refs := paramRefs(p.Query + "\n" + p.Provider.Connection + "\n" + string(options))
	var reads []string
	for _, name := range []string{"from", "to"} {
		if slices.Contains(refs, name) || slices.ContainsFunc(p.Params, func(def ParamDef) bool { return def.Name == name }) {
			reads = append(reads, name)
		}
	}
	for _, def := range p.Params {
		if def.Role == ParamRoleTimeFrom || def.Role == ParamRoleTimeTo {
			reads = append(reads, def.Name)
		}
	}
	return reads, nil
}
