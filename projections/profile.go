package projections

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/datetime"
)

// ProfileRunRequest is one read of a named Query profile: every page under All,
// with Params as the profile's key=value inputs.
type ProfileRunRequest struct {
	Name   string
	Params map[string]string
	All    bool
}

// ProfileRunResult is the part of a profile run a projection consumes and gates on.
type ProfileRunResult struct {
	Rows      []map[string]any
	Truncated bool
	HasMore   bool
	Metrics   *query.MetricMetadata
}

// ProfileRunner runs Query profiles for spec.source.query.profile. The query CLI
// implements it over its profile service; the library has no profile store.
type ProfileRunner interface {
	RunProfile(ctx context.Context, request ProfileRunRequest) (ProfileRunResult, error)
}

func parseProfileParams(params map[string]string) (map[string]*template.Template, error) {
	compiled := make(map[string]*template.Template, len(params))
	for key, value := range params {
		parsed, err := template.New(key).Option("missingkey=error").Parse(value)
		if err != nil {
			return nil, fmt.Errorf("spec.source.query.profile.params[%q]: %w", key, err)
		}
		compiled[key] = parsed
	}
	return compiled, nil
}

// profileSource is one run of a profile source: its runner and the single now
// every datemath start/end of the run is evaluated against.
type profileSource struct {
	runner ProfileRunner
	now    time.Time
}

func queryProfileProjection(ctx context.Context, p Projection, values map[string]any, source profileSource) ([]map[string]any, error) {
	q := p.Spec.Source.Query.Profile
	params, err := parseProfileParams(q.Params)
	if err != nil {
		return nil, err
	}
	entries := []map[string]any{nil}
	if q.Mode == "perTarget" {
		if entries, err = projectionEntries(p, values); err != nil {
			return nil, err
		}
	}
	rows := make([]map[string]any, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		payload, err := runProjectionProfile(ctx, *q, params, profileActivation(q.Mode, values, entry), source)
		if err != nil {
			return nil, err
		}
		if q.Mode == "perTarget" {
			rows = append(rows, projectionTargetRow(entry, payload))
			continue
		}
		normalized, err := projectionRows(payload)
		if err != nil {
			return nil, err
		}
		rows = append(rows, normalized...)
	}
	return rows, nil
}

func profileActivation(mode string, values, entry map[string]any) map[string]any {
	activation := map[string]any{"context": values}
	if mode == "perTarget" {
		activation["target"] = entry
	}
	return activation
}

// profileWindowParams are the params the completeness gate compares against
// metrics.sources; they are resolved to absolute instants before the run.
var profileWindowParams = []string{"start", "end"}

// resolveProfileInstant accepts an RFC3339 instant or datemath (now-1h, now-24h/h)
// evaluated against now, through the parser Query profiles use for date params.
func resolveProfileInstant(key, value string, now time.Time) (time.Time, error) {
	if datetime.IsDateMath(strings.TrimSpace(value)) {
		parsed, err := datetime.Parse(value, now)
		if err != nil {
			return time.Time{}, fmt.Errorf("param %s=%q: %w", key, value, err)
		}
		return parsed.Time, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("param %s=%q must be an RFC3339 instant or datemath such as now-1h", key, value)
	}
	return parsed, nil
}

func renderProfileRequest(q ProjectionProfileQuery, params map[string]*template.Template, activation map[string]any, now time.Time) (ProfileRunRequest, error) {
	request := ProfileRunRequest{Name: q.Name, All: q.All, Params: make(map[string]string, len(params))}
	for key, compiled := range params {
		var rendered strings.Builder
		if err := compiled.Execute(&rendered, activation); err != nil {
			return request, fmt.Errorf("profile param %q template: %w", key, err)
		}
		request.Params[key] = rendered.String()
	}
	for _, key := range profileWindowParams {
		value, ok := request.Params[key]
		if !ok {
			continue
		}
		instant, err := resolveProfileInstant(key, value, now)
		if err != nil {
			return request, fmt.Errorf("profile %s: %w", q.Name, err)
		}
		request.Params[key] = instant.UTC().Format(time.RFC3339Nano)
	}
	return request, nil
}

func runProjectionProfile(ctx context.Context, q ProjectionProfileQuery, params map[string]*template.Template, activation map[string]any, source profileSource) (any, error) {
	request, err := renderProfileRequest(q, params, activation, source.now)
	if err != nil {
		return nil, err
	}
	result, err := source.runner.RunProfile(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("profile %s: %w", q.Name, err)
	}
	if err := profileResultComplete(request, result); err != nil {
		return nil, fmt.Errorf("profile %s: %w", q.Name, err)
	}
	// Round-trip through JSON so profile rows carry the same value types exec rows do.
	encoded, err := json.Marshal(append([]map[string]any{}, result.Rows...))
	if err != nil {
		return nil, fmt.Errorf("profile %s rows: %w", q.Name, err)
	}
	var payload any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, fmt.Errorf("profile %s rows: %w", q.Name, err)
	}
	return selectPayload(payload, q.Select)
}

// profileResultComplete refuses a partial answer: a cut-short read, unread pages,
// metric notices, or metric sources evaluated over a window other than the
// requested start/end params.
func profileResultComplete(request ProfileRunRequest, result ProfileRunResult) error {
	if result.Truncated || result.HasMore {
		return fmt.Errorf("incomplete result (truncated=%t, hasMore=%t); set all: true or narrow the params", result.Truncated, result.HasMore)
	}
	if result.Metrics == nil {
		return nil
	}
	if len(result.Metrics.Notices) > 0 {
		notices := make([]string, 0, len(result.Metrics.Notices))
		for _, notice := range result.Metrics.Notices {
			notices = append(notices, notice.Severity+": "+notice.Message)
		}
		return fmt.Errorf("metric notices: %s", strings.Join(notices, "; "))
	}
	for _, key := range []string{"start", "end"} {
		if err := metricSourcesMatch(request.Params, key, result.Metrics.Sources); err != nil {
			return err
		}
	}
	return nil
}

func metricSourcesMatch(params map[string]string, key string, sources []query.MetricSource) error {
	raw, requested := params[key]
	if !requested || len(sources) == 0 {
		return nil
	}
	want, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return fmt.Errorf("param %s=%q was not resolved to an RFC3339 instant before the run: %w", key, raw, err)
	}
	for i, source := range sources {
		got := source.Start
		if key == "end" {
			got = source.End
		}
		if !got.Equal(want) {
			return fmt.Errorf("metrics.sources[%d].%s is %s, requested %s", i, key, got.Format(time.RFC3339Nano), raw)
		}
	}
	return nil
}

func requireProfileRunner(projections []Projection, runner ProfileRunner) error {
	if runner != nil {
		return nil
	}
	if i := slices.IndexFunc(projections, func(p Projection) bool { return p.Spec.Source.Query.Profile != nil }); i >= 0 {
		return fmt.Errorf("projection %s: spec.source.query.profile requires a profile runner (Options.Profiles)", projections[i].Metadata.Name)
	}
	return nil
}
