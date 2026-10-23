// Package traceserve serves the API of a trace results store: the
// traces/<kind> result profiles, the sessions API over its captures (their
// exports and checkpoints) and the profile OpenAPI document, as one handler a
// host mounts under a path of its own. It serves no UI: a host renders the
// store with clicky-ui or @flanksource/commons-db-ui, or composes the query
// app over the handler itself (www.Handler serves the app's pages, and the
// /traces/{kind} links to them). gavel mounts it over a run's store; `query
// serve` opens its own store through the same Open.
package traceserve

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/cmd/query/sessions"
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
//
// Every other path is 404.
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
	openAPI, err := profileService.OpenAPIHandler(&cobra.Command{Use: "query"}, nil)
	if err != nil {
		return nil, err
	}
	rest := http.NewServeMux()
	rest.Handle("GET /api/openapi.json", openAPI)
	rest.Handle("/", http.NotFoundHandler())
	profileHandler, err := profileService.Handler(APIPrefix, rest)
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

// ReopenOptions say which store Reopen serves.
type ReopenOptions struct {
	// Dir is the directory a closed store was left in, such as a finished
	// run's; Reopen never creates it.
	Dir string
	// Kinds declares the result types the store's streams hold.
	Kinds *tracing.Kinds
}

// reopenedRetention is the stream TTL a reopened store is opened with. A
// stream's expiry is set when it is first appended to, and nothing appends to
// a reopened store: it serves no capture, so the TTL never applies.
const reopenedRetention = time.Hour

// Reopen serves the store a closed host left in options.Dir, read-only
// (ReadOnly), with the close that stops using it. A store that is gone is
// os.ErrNotExist; one that is there but does not open is an error.
func Reopen(options ReopenOptions) (http.Handler, func() error, error) {
	info, err := os.Stat(options.Dir)
	if err != nil {
		return nil, nil, fmt.Errorf("reopen trace store: %w", err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("reopen trace store: %s is not a directory", options.Dir)
	}
	runtime, closeStore, err := Open(OpenOptions{Dir: options.Dir, Kinds: options.Kinds, Retention: reopenedRetention})
	if err != nil {
		return nil, nil, err
	}
	handler, err := Handler(Options{Runtime: runtime, Authorize: ReadOnly})
	if err != nil {
		return nil, nil, errors.Join(err, closeStore())
	}
	return handler, closeStore, nil
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
