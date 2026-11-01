package profiles

import (
	stdcontext "context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	clickymetrics "github.com/flanksource/clicky/metrics"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/profilestore"
)

// ProfileSeriesPrefix is the id prefix ProfileSource serves; register the
// source under it in a clicky metrics.Sources.
const ProfileSeriesPrefix = "profile"

// ProfileSource serves `profile:<name>?k=v&...` ids to clicky metrics panels:
// it windows the profile's provider range onto the request, executes it under
// the caller's permissions and projects the rows onto series.
type ProfileSource struct {
	service *Service
	options SeriesSourceOptions
	flight  singleflight.Group
}

// SeriesSourceOptions decide which requests a ProfileSource may answer with one
// execution.
type SeriesSourceOptions struct {
	// Scope names every input a read takes from its caller's context rather
	// than its id: the environment a connection reference resolves in, the
	// tenant a store is routed to, and — when BeforeExecute authorizes per
	// caller — the principal, since the profiles a read references through
	// `profiles.<name>` are only prepared for the caller that started it.
	// Requests share an execution only when their scopes are equal; an error
	// or an empty scope refuses the read.
	Scope func(ctx stdcontext.Context) (string, error)
	// Timeout bounds a shared execution, which no single caller's
	// cancellation ends.
	Timeout time.Duration
}

// SeriesSource is the service's ProfileSource. Every read runs the service's
// BeforeExecute hook under the caller's context first, as every other read of
// a profile's data does.
func (s *Service) SeriesSource(options SeriesSourceOptions) (*ProfileSource, error) {
	if options.Scope == nil {
		return nil, fmt.Errorf("profile series source requires a Scope naming what a read takes from its caller")
	}
	if options.Timeout <= 0 {
		return nil, fmt.Errorf("profile series source requires a positive Timeout, got %s", options.Timeout)
	}
	return &ProfileSource{service: s, options: options}, nil
}

func (p *ProfileSource) QuerySeries(ctx stdcontext.Context, req clickymetrics.SeriesRequest) ([]clickymetrics.Series, error) {
	if req.Step <= 0 {
		return nil, fmt.Errorf("profile series require a positive step, got %s", req.Step)
	}
	window := alignedWindow(req)
	if !window.end.After(window.start) {
		return nil, fmt.Errorf("profile series window %s..%s is shorter than one %s step", req.Since, req.Until, req.Step)
	}
	ids := slices.Compact(slices.Sorted(slices.Values(req.IDs)))
	results := make([][]clickymetrics.Series, len(ids))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(8)
	for i, id := range ids {
		group.Go(func() error {
			series, err := p.seriesFor(groupCtx, id, window)
			if err != nil {
				return fmt.Errorf("%s: %w", id, err)
			}
			results[i] = series
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return slices.Concat(results...), nil
}

type seriesWindow struct {
	start, end time.Time
	step       time.Duration
}

// alignedWindow floors both bounds to the step, Grafana-style, so a point keeps
// its timestamp from one tick to the next and a stream appends only new ones.
func alignedWindow(req clickymetrics.SeriesRequest) seriesWindow {
	return seriesWindow{start: req.Since.UTC().Truncate(req.Step), end: req.Until.UTC().Truncate(req.Step), step: req.Step}
}

func (p *ProfileSource) seriesFor(ctx stdcontext.Context, id string, window seriesWindow) ([]clickymetrics.Series, error) {
	name, values, err := parseProfileSeriesID(id)
	if err != nil {
		return nil, err
	}
	scope, err := p.options.Scope(ctx)
	if err != nil {
		return nil, fmt.Errorf("profile series scope: %w", err)
	}
	if scope == "" {
		return nil, fmt.Errorf("profile series scope: the request names an empty scope")
	}
	store, err := p.service.store()
	if err != nil {
		return nil, err
	}
	resolved, err := Resolve(ctx, store, name)
	if err != nil {
		return nil, err
	}
	profile, err := windowedProfile(resolved.Profile, window)
	if err != nil {
		return nil, err
	}
	params := seriesParams(profile, values)
	release, err := p.service.prepareRead(ctx, profile, params)
	if err != nil {
		return nil, err
	}
	defer release()
	// The key is taken after the hook, which may rewrite params; every caller
	// still passed its own hook before sharing the execution.
	encoded, err := canonicalSeriesParams(params)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%q\x00%s\x00%s\x00%d\x00%d\x00%d", scope, name, encoded, window.start.UnixNano(), window.end.UnixNano(), window.step)
	shared := p.flight.DoChan(key, func() (any, error) { return p.executeShared(ctx, store, profile, params) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case answer := <-shared:
		if answer.Err != nil {
			return nil, answer.Err
		}
		spec := query.SeriesSpec{}
		if profile.Series != nil {
			spec = *profile.Series
		}
		spec.Source = id
		return query.ProjectSeries(*answer.Val.(*query.Result), spec)
	}
}

// executeShared runs one execution for every caller sharing its key. It keeps
// the starting caller's values — its scope is every sharer's — but none of its
// cancellation, so that caller leaving does not fail the others; Timeout bounds
// it instead.
func (p *ProfileSource) executeShared(caller stdcontext.Context, store profilestore.Store, profile query.Profile, params map[string]any) (*query.Result, error) {
	detached := stdcontext.WithoutCancel(caller)
	bounded, cancel := stdcontext.WithTimeout(detached, p.options.Timeout)
	defer cancel()
	return query.Execute(WithProfiles(p.service.context().Wrap(bounded), store, p.service.beforeExecute, bounded), profile, params)
}

func parseProfileSeriesID(id string) (string, url.Values, error) {
	rest, ok := strings.CutPrefix(id, ProfileSeriesPrefix+":")
	if !ok {
		return "", nil, fmt.Errorf("profile series ids start with %q", ProfileSeriesPrefix+":")
	}
	escaped, rawQuery, _ := strings.Cut(rest, "?")
	name, err := url.PathUnescape(escaped)
	if err != nil || name == "" {
		return "", nil, fmt.Errorf("profile series id %q names no profile", id)
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", nil, fmt.Errorf("profile series id %q params: %w", id, err)
	}
	return name, values, nil
}

// windowedProfile replaces the provider's declared range with the request
// window. A profile without one would ignore the window and plot whatever it
// reads, so it is refused.
func windowedProfile(p query.Profile, window seriesWindow) (query.Profile, error) {
	if _, ok := p.Provider.Options["range"]; !ok {
		return query.Profile{}, fmt.Errorf("profile %q declares no provider range to window", p.Name)
	}
	p.Provider.Options = maps.Clone(p.Provider.Options)
	p.Provider.Options["range"] = map[string]any{
		"start": window.start.Format(time.RFC3339Nano), "end": window.end.Format(time.RFC3339Nano), "step": window.step.String(),
	}
	return p, nil
}

// seriesParams decodes id params as the execute route does: a list param keeps
// every value, a scalar its first.
func seriesParams(p query.Profile, values url.Values) map[string]any {
	params := map[string]any{}
	for key, list := range values {
		index := slices.IndexFunc(p.Params, func(def query.ParamDef) bool { return def.Name == key })
		if index >= 0 && p.Params[index].Type == query.ParamTypeList {
			params[key] = list
			continue
		}
		params[key] = list[0]
	}
	return params
}

// canonicalSeriesParams encodes params with sorted keys and typed values, so a
// list and a scalar that print alike never share a key.
func canonicalSeriesParams(params map[string]any) (string, error) {
	encoded, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("encode profile series params: %w", err)
	}
	return string(encoded), nil
}
