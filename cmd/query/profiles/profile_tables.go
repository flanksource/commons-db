package profiles

import (
	stdcontext "context"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/profilestore"
)

// WithProfiles binds `profiles.<name>` references executed under execCtx to
// store. Each reference is prepared by hook under caller — the context of
// whoever asked for the read — so a profile cannot read, through another, data
// its caller could not read directly. It is the one place a resolver is bound:
// every read the Service owns gets its execution context from queryContext or,
// for a handler deriving its context elsewhere, from this; a session gets it
// from WithSessionProfiles.
func WithProfiles(execCtx dbcontext.Context, store profilestore.Store, hook profilestore.BeforeExecuteFunc, caller stdcontext.Context) dbcontext.Context {
	return query.WithProfileResolver(execCtx, query.ProfileResolver{
		Resolve: storeResolver(store),
		Prepare: func(_ stdcontext.Context, p query.Profile, params map[string]any) (func(), error) {
			return profilestore.PrepareReads(caller, hook, []profilestore.ReadRequest{{Profile: p, Params: params}})
		},
	})
}

// WithSessionProfiles binds the `profiles.<name>` references a top or trace
// session started under execCtx reads to store. Each is prepared by registry's
// BeforeRead under the context the reference is read in — the session's run
// context, which keeps the starting caller's values — as the session's own
// read is, at its start and before every later sample.
func WithSessionProfiles(execCtx dbcontext.Context, store profilestore.Store, registry *query.SessionRegistry) dbcontext.Context {
	return query.WithProfileResolver(execCtx, query.ProfileResolver{
		Resolve: storeResolver(store),
		Prepare: registry.PrepareRead,
	})
}

func storeResolver(store profilestore.Store) func(ctx stdcontext.Context, name string) (query.Profile, error) {
	return func(ctx stdcontext.Context, name string) (query.Profile, error) {
		resolved, err := Resolve(ctx, store, name)
		return resolved.Profile, err
	}
}

// queryContext is the context a read the Service owns executes under: the
// service's context over caller, with `profiles.<name>` bound to store — the
// one the read resolved its profile from.
func (s *Service) queryContext(caller stdcontext.Context, store profilestore.Store) dbcontext.Context {
	return WithProfiles(s.context().Wrap(caller), store, s.beforeExecute, caller)
}
