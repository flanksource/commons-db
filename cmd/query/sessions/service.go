package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

type ProfileStoreProvider func() (profiles.Store, error)
type ContextProvider func() dbcontext.Context

// SessionEventLog holds the events a capture stream session emitted through
// the registry's EventSink, for replay after the session left the registry.
type SessionEventLog interface {
	Events(ctx context.Context, id string) ([]query.Event, error)

	// HasEvents reports whether the log holds any event of session id, which
	// is what a session's eventsAvailable says without reading them all.
	HasEvents(ctx context.Context, id string) (bool, error)
}

// EventRecords reads the record streams a session's status.events names:
// recordstore.Notifier is one.
type EventRecords interface {
	Meta(ctx context.Context, stream string) (recordstore.Meta, error)
	Scan(ctx context.Context, stream string, afterSeq int64, fn func(seq int64, row recordstore.Row) error) error
	Tail(ctx context.Context, stream string, afterSeq int64, fn func(seq int64, row recordstore.Row) error) error
}

type Options struct {
	Profiles ProfileStoreProvider
	Context  ContextProvider
	Registry *query.SessionRegistry

	// Store answers for sessions that are not live in the registry: the list,
	// info, lineage, and the records restart and replay start from. Nil lists
	// only the registry's sessions.
	Store query.SessionStore

	// EventLog replays the events of a stream session that recorded no
	// status.events, after it left the registry.
	EventLog SessionEventLog

	// Records replays a session's status.events after its owner process is
	// gone, and follows it while this process still writes it.
	Records EventRecords

	// Authorize decides whether r may read or control a session of the named
	// profile. It is asked before a session starts, stops, extends or restarts
	// (ActionControl) and before a session's info, events or result is served
	// (ActionRead): routes that name no profile, so a gate on profile paths
	// alone cannot cover them. A session the caller may not read is answered
	// 404, as a missing one is, so its id never confirms it exists; any other
	// refusal is answered 403 with its error. The list, its totals, its
	// lookups and restartedAs leave out refused profiles. Nil allows
	// every caller everything, which is right only when every profile served
	// has the same permission. A connection trace session is named by its
	// virtual profile, connection-<id>-sql-xevent.
	Authorize AuthorizeFunc

	// Principal names the caller of r, for a stop reason and the principal of
	// a restarted session. Nil stops "via API" and restarts as the previous
	// principal.
	Principal func(r *http.Request) string
}

type Service struct {
	options Options
}

func New(options Options) (*Service, error) {
	if options.Profiles == nil {
		return nil, fmt.Errorf("profile store provider is required")
	}
	if options.Context == nil {
		return nil, fmt.Errorf("context provider is required")
	}
	if options.Registry == nil {
		return nil, fmt.Errorf("session registry is required")
	}
	return &Service{options: options}, nil
}

func (s *Service) Handler(prefix string, next http.Handler) (http.Handler, error) {
	store, err := s.options.Profiles()
	if err != nil {
		return nil, err
	}
	o := s.options
	return newSessionHandler(sessionHandlerOptions{
		Prefix: prefix, Ctx: o.Context(), Store: store, Registry: o.Registry, Sessions: o.Store,
		EventLog: o.EventLog, Records: o.Records, Authorize: o.Authorize, Principal: o.Principal, Next: next,
	}), nil
}

// sessionHandler serves the session API:
//
//	POST {prefix}/profile/{name}/sessions        start (?interval samples, ?follow tails)
//	POST {prefix}/connection/{id}/trace/sessions start a connection trace
//	GET  {prefix}/sessions                       list (filters, window, sort, paging, ?__lookup=filters)
//	GET  {prefix}/sessions/{id}                  info
//	POST {prefix}/sessions/{id}/stop             stop a live session
//	POST {prefix}/sessions/{id}/extend           move a live session's deadline (?duration)
//	POST {prefix}/sessions/{id}/restart          start a new session from an ended one (?duration)
//	GET  {prefix}/sessions/{id}/events           SSE, resumable via Last-Event-ID (?format=ndjson exports)
//	GET  {prefix}/sessions/{id}/result           materialized rows, or the recorded result
type sessionHandler struct {
	prefix    string
	ctx       dbcontext.Context
	store     profiles.Store
	registry  *query.SessionRegistry
	sessions  query.SessionStore
	eventLog  SessionEventLog
	records   EventRecords
	authorize AuthorizeFunc
	principal func(r *http.Request) string
	next      http.Handler
}

type sessionHandlerOptions struct {
	Prefix    string
	Ctx       dbcontext.Context
	Store     profiles.Store
	Registry  *query.SessionRegistry
	Sessions  query.SessionStore
	EventLog  SessionEventLog
	Records   EventRecords
	Authorize AuthorizeFunc
	Principal func(r *http.Request) string
	Next      http.Handler
}

func newSessionHandler(opts sessionHandlerOptions) *sessionHandler {
	return &sessionHandler{
		prefix: strings.TrimRight(opts.Prefix, "/"), ctx: opts.Ctx, store: opts.Store, registry: opts.Registry,
		sessions: opts.Sessions, eventLog: opts.EventLog, records: opts.Records, authorize: opts.Authorize,
		principal: opts.Principal, next: opts.Next,
	}
}

func (h *sessionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rel := strings.Trim(strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/"), h.prefix), "/")
	parts := strings.Split(rel, "/")
	get, post := r.Method == http.MethodGet, r.Method == http.MethodPost
	switch {
	case post && len(parts) == 4 && parts[0] == "connection" && parts[2] == "trace" && parts[3] == "sessions":
		h.startConnectionTrace(w, r, parts[1])
	case post && len(parts) == 3 && parts[0] == "profile" && parts[2] == "sessions":
		h.start(w, r, parts[1])
	case parts[0] != "sessions":
		h.next.ServeHTTP(w, r)
	case get && len(parts) == 1:
		h.list(w, r)
	case get && len(parts) == 2:
		h.info(w, r, parts[1])
	case post && len(parts) == 3 && parts[2] == "stop":
		h.stop(w, r, parts[1])
	case post && len(parts) == 3 && parts[2] == "extend":
		h.extend(w, r, parts[1])
	case post && len(parts) == 3 && parts[2] == "restart":
		h.restart(w, r, parts[1])
	case get && len(parts) == 3 && parts[2] == "events":
		h.events(w, r, parts[1])
	case get && len(parts) == 3 && parts[2] == "result":
		h.result(w, r, parts[1])
	default:
		h.next.ServeHTTP(w, r)
	}
}

// queryFlag reads a boolean query parameter in both the forms a caller writes
// it: `?follow` and `?follow=true`. A value that is neither is refused rather
// than read as false, because a misspelled flag that quietly does nothing is
// indistinguishable from a feature that does not work.
func queryFlag(values url.Values, key string) (bool, error) {
	raw, ok := values[key]
	if !ok || len(raw) == 0 {
		return false, nil
	}
	if raw[0] == "" {
		return true, nil
	}
	flag, err := strconv.ParseBool(raw[0])
	if err != nil {
		return false, fmt.Errorf("invalid %s %q: expected a boolean", key, raw[0])
	}
	return flag, nil
}

// sessionReservedParam extends reservedParam with the session transport keys.
func sessionReservedParam(key string) bool {
	return profiles.IsReservedParam(key) || key == "interval" || key == "duration" || key == "follow"
}

func writeSessionJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}
