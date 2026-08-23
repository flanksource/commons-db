package inspect

// Who paid for the metadata, and did they pay in full.
//
// A Memo already knows whether a lookup was served from its entry or filled
// from the source, how old the entry was and whether the last refresh failed —
// and returns all of it on Result.Cache. What it cannot know is *whose* request
// asked, which is exactly what a debug console needs in order to say "this page
// was slow because it discovered 4000 OpenSearch fields".
//
// So the observer rides on the context rather than on the Memo: the Memos are
// package-level and shared by every request, and a field on one of them could
// only ever report to a single listener. This package stays free of any
// dependency on who is listening.

import (
	"context"
	"time"
)

// Observation is one inspection-cache lookup as the caller experienced it.
type Observation struct {
	// Policy is the cache class (see CacheClass), and Key the entry within it.
	Policy string
	Key    string

	// Elapsed is the wall time this caller waited. A cached read is ~0; a miss
	// pays the whole fill, which is the number worth looking at.
	Elapsed time.Duration

	Cache CacheMetadata

	// Err is the fill failure, if the caller got one. A stale entry served after
	// a failed refresh reports the failure on Cache.LastRefreshError instead and
	// leaves this empty — the caller was not failed, only served older facts.
	Err error
}

// Observer receives every lookup made under a context it was installed on. It
// is called on the caller's goroutine, so it must not block.
type Observer func(Observation)

// ObserverKey is the context key an observer is stored under.
//
// Exported because the request contexts that install one are not plain
// context.Context — commons-db's Context sets values through its own WithValue
// and would have to be rebuilt around a stdlib child otherwise, losing the
// database handle and namespace it carries.
type ObserverKey struct{}

// WithObserver returns a context whose inspection lookups report to observe.
//
// Installing a second observer replaces the first rather than chaining: the
// owner of a request context is the one party that knows what should hear about
// its lookups, and a hidden chain would let an inner scope leak observations to
// an outer one after it went out of scope.
func WithObserver(ctx context.Context, observe Observer) context.Context {
	if observe == nil {
		return ctx
	}
	return context.WithValue(ctx, ObserverKey{}, observe)
}

// ObserverFrom returns the observer installed on ctx, or nil.
func ObserverFrom(ctx context.Context) Observer {
	observer, _ := ctx.Value(ObserverKey{}).(Observer)
	return observer
}

func observe(ctx context.Context, observation Observation) {
	if observer := ObserverFrom(ctx); observer != nil {
		observer(observation)
	}
}

// RefreshKey marks a context whose inspection lookups must all rebuild rather
// than read what is cached.
//
// Exported for the same reason ObserverKey is: the request contexts that set it
// are commons-db's Context, which carries a database handle and a namespace it
// would lose if it had to be rebuilt around a stdlib child.
//
// It is deliberately per-request. GetOptions.Refresh already says "rebuild this
// one lookup" for a caller that knows which; this says "rebuild everything I
// read", which is what someone debugging a page wants and is still nobody
// else's problem — unlike flushing the cache, which is (see Flush).
type RefreshKey struct{}

// WithRefresh returns a context whose inspection lookups all rebuild.
func WithRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, RefreshKey{}, true)
}

// RefreshRequested reports whether ctx asked for every lookup to rebuild.
func RefreshRequested(ctx context.Context) bool {
	refresh, _ := ctx.Value(RefreshKey{}).(bool)
	return refresh
}
