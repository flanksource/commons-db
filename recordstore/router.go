package recordstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// RouterOptions configure NewRouter.
type RouterOptions struct {
	// Route names the route a call's context belongs to — the tenant, the
	// environment — and fails for a context that carries none.
	Route func(ctx context.Context) (string, error)

	// Open opens one route's backend. It is called once per route, and again
	// only after Forget drops it. The backend it returns must belong to that
	// route alone: never shared with another route, and never the index a
	// registry reads, or one route's streams become readable through another.
	Open func(ctx context.Context, route string) (Backend, error)
}

// Router is a Backend that resolves, per call, the backend of the route the
// call's context names, opening it on first use and keeping it — so a
// backend's per-stream append serialization holds across calls.
//
// A stream id is not qualified by its route: routes are kept apart by each
// owning its backend, so a stream written on one route is ErrNotFound on
// every other.
type Router struct {
	options RouterOptions

	mu     sync.Mutex
	routes map[string]Backend
	closed bool
}

var _ Backend = (*Router)(nil)

// NewRouter routes calls by options.Route to backends options.Open opens.
func NewRouter(options RouterOptions) (*Router, error) {
	switch {
	case options.Route == nil:
		return nil, errors.New("record store router: Route is required")
	case options.Open == nil:
		return nil, errors.New("record store router: Open is required")
	}
	return &Router{options: options, routes: map[string]Backend{}}, nil
}

// Resolve is the backend of the route ctx names, opened on first use: every
// call delegates to it, and a caller asking where a stream lives asks it.
func (r *Router) Resolve(ctx context.Context) (Backend, error) {
	route, err := r.options.Route(ctx)
	if err != nil {
		return nil, fmt.Errorf("route the record store call: %w", err)
	}
	if route == "" {
		return nil, errors.New("route the record store call: the route is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, fmt.Errorf("record store route %q: the router is closed", route)
	}
	if backend, ok := r.routes[route]; ok {
		return backend, nil
	}
	backend, err := r.options.Open(ctx, route)
	if err != nil {
		return nil, fmt.Errorf("open record store route %q: %w", route, err)
	}
	if backend == nil {
		return nil, fmt.Errorf("open record store route %q: it opened no backend", route)
	}
	r.routes[route] = backend
	return backend, nil
}

func (r *Router) Append(ctx context.Context, stream, kind string, rows []Row) (AppendResult, error) {
	backend, err := r.Resolve(ctx)
	if err != nil {
		return AppendResult{}, err
	}
	return backend.Append(ctx, stream, kind, rows)
}

func (r *Router) Trim(ctx context.Context, stream string, before time.Time) (Meta, error) {
	backend, err := r.Resolve(ctx)
	if err != nil {
		return Meta{}, err
	}
	return backend.Trim(ctx, stream, before)
}

func (r *Router) Meta(ctx context.Context, stream string) (Meta, error) {
	backend, err := r.Resolve(ctx)
	if err != nil {
		return Meta{}, err
	}
	return backend.Meta(ctx, stream)
}

func (r *Router) Scan(ctx context.Context, stream string, afterSeq int64, fn func(int64, Row) error) error {
	backend, err := r.Resolve(ctx)
	if err != nil {
		return err
	}
	return backend.Scan(ctx, stream, afterSeq, fn)
}

func (r *Router) Expire(ctx context.Context, stream string, ttl time.Duration) error {
	backend, err := r.Resolve(ctx)
	if err != nil {
		return err
	}
	return backend.Expire(ctx, stream, ttl)
}

func (r *Router) Seal(ctx context.Context, stream string) error {
	backend, err := r.Resolve(ctx)
	if err != nil {
		return err
	}
	return backend.Seal(ctx, stream)
}

func (r *Router) Delete(ctx context.Context, stream string) error {
	backend, err := r.Resolve(ctx)
	if err != nil {
		return err
	}
	return backend.Delete(ctx, stream)
}

// Forget drops and closes route's backend, so the next call on the route
// opens it afresh — for an owner whose store behind the route went away. It
// does nothing unless the route still holds backend (compared by identity), so
// an old owner releasing late cannot evict the backend its replacement opened.
// A call already holding the dropped backend finishes against it.
func (r *Router) Forget(route string, backend Backend) error {
	r.mu.Lock()
	current, ok := r.routes[route]
	if !ok || current != backend {
		r.mu.Unlock()
		return nil
	}
	delete(r.routes, route)
	r.mu.Unlock()
	if err := current.Close(); err != nil {
		return fmt.Errorf("close record store route %q: %w", route, err)
	}
	return nil
}

// Close closes every route's backend and refuses calls after it.
func (r *Router) Close() error {
	r.mu.Lock()
	routes := r.routes
	r.routes = map[string]Backend{}
	r.closed = true
	r.mu.Unlock()
	var errs []error
	for route, backend := range routes {
		if err := backend.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close record store route %q: %w", route, err))
		}
	}
	return errors.Join(errs...)
}
