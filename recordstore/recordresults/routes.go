// Routed results: a stream of a Router's route is indexed under
// <route>:<stream>, so streams two routes share an id for stay apart, and a
// read may span only the routes its caller was granted.
package recordresults

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/commons-db/query/profilestore"
	"github.com/flanksource/commons-db/recordstore"
)

// streamsParam lists the streams a read across streams reads, and
// streamsProfile ends the name of a result type's profile that takes it.
const (
	streamsParam   = "streams"
	streamsProfile = "/streams"
	routeSeparator = ":"

	// sourceStreamParam records, in a read's params, the stream the caller
	// named once BeforeExecute bound the stream param to its routed index id,
	// so preparing the params again or following them finds the source stream.
	sourceStreamParam = "recordstoreSourceStream"
)

type routesKey struct{}

// WithRoutes grants a read under ctx the routes it lists, beyond the route ctx
// itself names. A host adds them where it authorizes the caller: a read
// across streams reads only its own route and these, and none by default.
func WithRoutes(ctx context.Context, routes ...string) context.Context {
	granted := append(slices.Clone(grantedRoutes(ctx)), routes...)
	return context.WithValue(ctx, routesKey{}, granted)
}

func grantedRoutes(ctx context.Context) []string {
	routes, _ := ctx.Value(routesKey{}).([]string)
	return routes
}

// mirror is an index stream and the source stream it mirrors: the routed
// backend holding it, or none for the registry's own source.
type mirror struct {
	source recordstore.Backend
	stream string
	index  string
}

// ensure catches the index stream up with its source.
func (r *Registry) ensure(ctx context.Context, target mirror) error {
	if target.source == nil {
		return r.indexer.Ensure(ctx, target.stream)
	}
	return r.indexer.EnsureAs(ctx, target.source, target.stream, target.index)
}

// ownStream is the mirror of stream as ctx reads it: itself, or for a routed
// registry the stream of the route ctx names.
func (r *Registry) ownStream(ctx context.Context, stream string) (mirror, error) {
	if r.router == nil {
		return mirror{stream: stream, index: stream}, nil
	}
	route, err := r.router.Route(ctx)
	if err != nil {
		return mirror{}, err
	}
	return r.routeStream(ctx, route, stream)
}

// listedStream is the mirror of an entry of a streams param: a stream id, or
// for a routed registry <route>:<stream> of a route the caller may read.
func (r *Registry) listedStream(ctx context.Context, entry string) (mirror, error) {
	if r.router == nil {
		if err := recordstore.ValidateStream(entry); err != nil {
			return mirror{}, fmt.Errorf("%w: %w", profilestore.ErrProfileRequestInvalid, err)
		}
		return mirror{stream: entry, index: entry}, nil
	}
	route, stream, ok := strings.Cut(entry, routeSeparator)
	if !ok || route == "" {
		return mirror{}, fmt.Errorf("%w: stream %q names no route; list routed streams as <route>%s<stream>",
			profilestore.ErrProfileRequestInvalid, entry, routeSeparator)
	}
	own, err := r.router.Route(ctx)
	if (err != nil || route != own) && !slices.Contains(grantedRoutes(ctx), route) {
		return mirror{}, fmt.Errorf("%w: route %q was not granted to this read", profilestore.ErrProfileForbidden, route)
	}
	return r.routeStream(ctx, route, stream)
}

func (r *Registry) routeStream(ctx context.Context, route, stream string) (mirror, error) {
	if strings.Contains(route, routeSeparator) {
		return mirror{}, fmt.Errorf("record results: route %q holds %q, which qualifies index streams", route, routeSeparator)
	}
	index := route + routeSeparator + stream
	if err := recordstore.ValidateStream(index); err != nil {
		return mirror{}, fmt.Errorf("%w: %w", profilestore.ErrProfileRequestInvalid, err)
	}
	backend, err := r.router.Backend(ctx, route)
	if err != nil {
		return mirror{}, err
	}
	return mirror{source: backend, stream: stream, index: index}, nil
}

// requestedStreams is the entries of a read's streams param.
func requestedStreams(profile string, params map[string]any) ([]string, error) {
	// The param arrives as the request wrote it: one comma list, or a value
	// per repetition, each of which may itself be a comma list.
	var values []string
	switch value := params[streamsParam].(type) {
	case []string:
		values = value
	case []any:
		for _, entry := range value {
			values = append(values, fmt.Sprint(entry))
		}
	case string:
		values = []string{value}
	}
	var entries []string
	for _, value := range values {
		entries = append(entries, strings.Split(value, ",")...)
	}
	entries = slices.DeleteFunc(entries, func(entry string) bool { return strings.TrimSpace(entry) == "" })
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: profile %q reads record streams; list their ids as the %s param",
			profilestore.ErrProfileRequestInvalid, profile, streamsParam)
	}
	for index := range entries {
		entries[index] = strings.TrimSpace(entries[index])
	}
	return entries, nil
}

// boundSourceStream is the source stream of a read whose stream param its read
// hook bound: the marker param's, or, where resolution dropped that, the
// routed index id less the caller's own route.
func (r *Registry) boundSourceStream(ctx context.Context, params map[string]any, stream string) (string, error) {
	if source, bound := params[sourceStreamParam].(string); bound {
		return source, nil
	}
	if r.router == nil {
		return stream, nil
	}
	route, err := r.router.Route(ctx)
	if err != nil {
		return "", err
	}
	source, bound := strings.CutPrefix(stream, route+routeSeparator)
	if !bound {
		return "", fmt.Errorf("%w: stream %q is not one of route %q's", profilestore.ErrProfileRequestInvalid, stream, route)
	}
	return source, nil
}
