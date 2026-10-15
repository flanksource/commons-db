// Package traceserve serves a trace results store through query: the
// traces/<kind> result profiles, the sessions API over its captures (their
// exports and checkpoints) and the query app, as one handler a host mounts
// under a path of its own. gavel mounts it over a run's store; `query serve`
// opens its own store through the same Open.
package traceserve

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/cmd/query/sessions"
	"github.com/flanksource/commons-db/cmd/query/www"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/profilestore"

	// A store's result profiles page through the sqlite provider.
	_ "github.com/flanksource/commons-db/query/providers/sqlite"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/probe"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing"
)

const (
	// Prefix names a store's result profiles, traces/<kind>, and its index
	// connection, connection://traces/traces.
	Prefix = "traces"
	// ProfilePrefix is the name of a kind's result profile less the kind.
	ProfilePrefix = tracing.ProfilePrefix
	// APIPrefix is where Handler serves the profile and sessions APIs, under
	// the path the host mounts it at.
	APIPrefix = "/api/v1"
)

// OpenOptions say which store Open opens and what it serves.
type OpenOptions struct {
	// Dir is the store's directory; Open creates it when it does not exist.
	Dir string
	// Kinds declares the result types the store's streams hold.
	Kinds *tracing.Kinds
	// Retention is how long a stream is kept; an expired one is swept away.
	Retention time.Duration
}

// Open opens the trace results store in options.Dir and returns the runtime
// that serves it and starts captures into it, with a session registry of its
// own a host may replace, and the close that stops using the store.
func Open(options OpenOptions) (*tracing.Runtime, func() error, error) {
	if options.Kinds == nil {
		return nil, nil, errors.New("open trace store: the kinds it holds are required")
	}
	results, err := recordresults.Open(recordresults.OpenOptions{
		Prefix: Prefix, ConnectionName: Prefix, Register: options.Kinds.RegisterResultTypes,
		Settings: recordstore.Settings{Prefix: Prefix, Backend: recordstore.BackendSQLite, Dir: options.Dir, TTL: options.Retention},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("open trace store %s: %w", options.Dir, err)
	}
	return &tracing.Runtime{
		Kinds: options.Kinds, Results: results,
		Probes:   probe.NewManager(probe.ManagerOptions{LockDir: filepath.Join(options.Dir, "locks")}),
		Sessions: query.NewSessionRegistry(query.RegistryOptions{BeforeRead: results.Registry.BeforeRead}),
	}, results.Close, nil
}

// Options configure Handler.
type Options struct {
	// Runtime is the open store served, and the captures running into it.
	Runtime *tracing.Runtime
	// Authorize decides what a caller may do with a capture; ReadOnly serves
	// the store without letting a caller start or stop one. Nil allows all.
	Authorize sessions.AuthorizeFunc
}

// Handler serves options.Runtime's store, rooted at the path the host mounts
// it at (strip that prefix before it):
//
//	GET  /api/openapi.json                       the result profiles and session routes
//	GET  /api/v1/profile/traces%2F{kind}         a window of a stream (?stream, ?afterSeq, ?toSeq)
//	GET  /api/v1/traces/{kind}/export            a window downloaded (?stream, ?afterSeq, ?toSeq, ?format)
//	POST /api/v1/sessions/{id}/checkpoint        a running capture's reading, committed
//	     /api/v1/sessions...                     the sessions API over the captures
//	GET  /traces/{kind}                          the app's page for a window (?stream, ?afterSeq, ?toSeq)
//	GET  /...                                    the query app
func Handler(options Options) (http.Handler, error) {
	runtime := options.Runtime
	if runtime == nil || runtime.Results == nil || runtime.Sessions == nil {
		return nil, errors.New("serve trace store: an open runtime with a session registry is required")
	}
	registry := runtime.Results.Registry
	store := func() (profilestore.Store, error) { return registry, nil }
	queryContext := dbcontext.New().WithConnectionResolver(registry.ResolveConnection)
	contextOf := func() dbcontext.Context { return queryContext }
	profileService, err := profiles.New(profiles.Options{
		Store: store, Context: contextOf, DecodeBody: profiles.DecodeRequestBody, BeforeExecute: registry.BeforeExecute,
		OpenAPIExtensions: []profiles.OpenAPIExtension{
			profiles.AddSessionsOpenAPI, sessions.AddTracesOpenAPI(func() *tracing.Runtime { return runtime }),
		},
	})
	if err != nil {
		return nil, err
	}
	app, err := appHandler(profileService)
	if err != nil {
		return nil, err
	}
	profileHandler, err := profileService.Handler(APIPrefix, app)
	if err != nil {
		return nil, err
	}
	sessionService, err := sessions.New(sessions.Options{
		Profiles: store, Context: contextOf, Registry: runtime.Sessions,
		Records: runtime.Results.Backend, Traces: runtime, Authorize: options.Authorize,
	})
	if err != nil {
		return nil, err
	}
	return sessionService.Handler(APIPrefix, profileHandler)
}

// appHandler serves the OpenAPI document the app discovers the profiles from,
// the app itself, and a 404 for any other API route, which the app's
// client-side routes must not answer.
func appHandler(profileService *profiles.Service) (http.Handler, error) {
	openAPI, err := profileService.OpenAPIHandler(&cobra.Command{Use: "query"}, nil)
	if err != nil {
		return nil, err
	}
	app, err := www.Handler()
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/api/openapi.json", openAPI)
	mux.Handle("/api/", http.NotFoundHandler())
	mux.HandleFunc("GET /traces/{kind}", openTrace)
	mux.Handle("/", app)
	return mux, nil
}

// openTrace sends a link to a kind's window, /traces/{kind}?stream&afterSeq&toSeq,
// on to the app's page for the kind's result profile with the same query, so a
// host links to a window without knowing how the app names its pages. The
// redirect is relative, so it stays under the path the host mounted the app at.
func openTrace(w http.ResponseWriter, r *http.Request) {
	target := "../" + profiles.SurfaceKey(ProfilePrefix+r.PathValue("kind"))
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	w.Header().Set("Location", target)
	w.WriteHeader(http.StatusFound)
}

// ReadOnly is an Authorize that serves a store's captures without letting a
// caller start, stop, extend or restart one; it may read them and have a
// running one checkpoint.
func ReadOnly(_ *http.Request, profile string, action sessions.Action) error {
	if action == sessions.ActionControl {
		return fmt.Errorf("%s: this trace store is served read-only", profile)
	}
	return nil
}
