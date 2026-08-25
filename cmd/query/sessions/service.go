package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/commons-db/cmd/query/internal/sse"
	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
)

type ProfileStoreProvider func() (profiles.Store, error)
type ContextProvider func() dbcontext.Context

type Options struct {
	Profiles ProfileStoreProvider
	Context  ContextProvider
	Registry *query.SessionRegistry
	Store    *Store
}

type Service struct {
	profiles ProfileStoreProvider
	context  ContextProvider
	registry *query.SessionRegistry
	store    *Store
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
	return &Service{profiles: options.Profiles, context: options.Context, registry: options.Registry, store: options.Store}, nil
}

func (s *Service) Handler(prefix string, next http.Handler) (http.Handler, error) {
	store, err := s.profiles()
	if err != nil {
		return nil, err
	}
	return newSessionHandler(sessionHandlerOptions{
		Prefix: prefix, Ctx: s.context(), Store: store, Registry: s.registry, Sessions: s.store, Next: next,
	}), nil
}

// sessionHandler serves the trace/top session lifecycle:
//
//	POST   {prefix}/profile/{name}/sessions   start (?interval samples, ?follow tails)
//	GET    {prefix}/sessions                  list (live ∪ persisted)
//	GET    {prefix}/sessions/{id}             info
//	DELETE {prefix}/sessions/{id}             stop
//	GET    {prefix}/sessions/{id}/events      SSE stream, resumable via Last-Event-ID
//	                                          (?format=ndjson exports)
//	GET    {prefix}/sessions/{id}/result      materialized rows
//
// Live sessions are served from the in-memory registry; the optional
// SessionStore answers for sessions that outlived the process.
type sessionHandler struct {
	prefix   string
	ctx      dbcontext.Context
	store    profiles.Store
	registry *query.SessionRegistry
	sessions *Store
	next     http.Handler
}

type sessionHandlerOptions struct {
	Prefix   string
	Ctx      dbcontext.Context
	Store    profiles.Store
	Registry *query.SessionRegistry
	Sessions *Store
	Next     http.Handler
}

func newSessionHandler(opts sessionHandlerOptions) *sessionHandler {
	return &sessionHandler{
		prefix:   strings.TrimRight(opts.Prefix, "/"),
		ctx:      opts.Ctx,
		store:    opts.Store,
		registry: opts.Registry,
		sessions: opts.Sessions,
		next:     opts.Next,
	}
}

func (h *sessionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rel := strings.Trim(strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/"), h.prefix), "/")
	parts := strings.Split(rel, "/")
	switch {
	case r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "profile" && parts[2] == "sessions":
		h.start(w, r, parts[1])
	case parts[0] == "sessions" && len(parts) == 1 && r.Method == http.MethodGet:
		h.list(w, r)
	case parts[0] == "sessions" && len(parts) == 2:
		h.session(w, r, parts[1])
	case parts[0] == "sessions" && len(parts) == 3 && parts[2] == "events" && r.Method == http.MethodGet:
		h.events(w, r, parts[1])
	case parts[0] == "sessions" && len(parts) == 3 && parts[2] == "result" && r.Method == http.MethodGet:
		h.result(w, r, parts[1])
	default:
		h.next.ServeHTTP(w, r)
	}
}

func (h *sessionHandler) start(w http.ResponseWriter, r *http.Request, name string) {
	resolved, err := profiles.Resolve(r.Context(), h.store, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	follow, err := queryFlag(r.URL.Query(), "follow")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p := resolved.Profile
	if p, err = applySessionSpecOverrides(p, sessionSpec{
		Interval: r.URL.Query().Get("interval"),
		Duration: r.URL.Query().Get("duration"),
		Follow:   follow,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	params := map[string]any{}
	for k, vs := range r.URL.Query() {
		if sessionReservedParam(k) || len(vs) == 0 {
			continue
		}
		params[k] = vs[0]
	}

	session, err := query.ExecuteStream(h.ctx, h.registry, p, params)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, query.ErrMaxSessions) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeSessionJSON(w, http.StatusCreated, session.Snapshot())
}

// sessionSpec is the transport's side of a session request: the HTTP query
// params, or the equivalent CLI flags.
type sessionSpec struct {
	Interval string
	Duration string
	Follow   bool
}

// applySessionSpecOverrides maps the transport inputs onto the profile: follow
// tails any plain profile as a trace, interval samples one as top (or overrides
// a declared interval), and duration lowers the session bound (the registry
// still clamps it).
//
// Follow is applied first so that asking for both it and an interval is caught
// by the trace/interval guard below rather than needing a rule of its own —
// they are two answers to one question, and a profile cannot be sampled on a
// clock and tailed continuously at the same time.
func applySessionSpecOverrides(p query.Profile, spec sessionSpec) (query.Profile, error) {
	if spec.Follow {
		// Asked here rather than left to ExecuteStream so the refusal names the
		// capability the caller asked for. Falling back to polling would answer a
		// question nobody asked: an interval is a different session with a
		// different cost, and choosing it silently hides that the provider cannot
		// do what the Follow control offered.
		if !query.SupportsStreaming(p.Provider.Type) {
			return p, fmt.Errorf("profile %q cannot be followed: provider %q does not stream; pass ?interval to sample it instead",
				p.Name, p.Provider.Type)
		}
		p = query.Follow(p)
	}
	if spec.Interval != "" {
		d, err := time.ParseDuration(spec.Interval)
		if err != nil {
			return p, fmt.Errorf("invalid interval %q: %w", spec.Interval, err)
		}
		if p.Kind() == query.KindTrace {
			return p, fmt.Errorf("profile %q is a trace; interval does not apply", p.Name)
		}
		if p.Top == nil {
			p.Top = &query.TopSpec{}
		}
		p.Top.Interval = types.Duration{Duration: d}
	}
	if p.Kind() == query.KindQuery {
		return p, fmt.Errorf("profile %q declares neither trace nor top; pass ?follow to tail it or ?interval to sample it", p.Name)
	}
	if spec.Duration != "" {
		d, err := time.ParseDuration(spec.Duration)
		if err != nil {
			return p, fmt.Errorf("invalid duration %q: %w", spec.Duration, err)
		}
		if p.Trace != nil {
			p.Trace.MaxDuration = types.Duration{Duration: d}
		} else {
			p.Top.MaxDuration = types.Duration{Duration: d}
		}
	}
	return p, nil
}

func (h *sessionHandler) list(w http.ResponseWriter, r *http.Request) {
	infos := h.registry.List()
	if h.sessions != nil {
		live := map[string]struct{}{}
		for _, info := range infos {
			live[info.ID] = struct{}{}
		}
		persisted, err := h.sessions.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, info := range persisted {
			if _, ok := live[info.ID]; !ok {
				infos = append(infos, info)
			}
		}
	}
	writeSessionJSON(w, http.StatusOK, infos)
}

func (h *sessionHandler) session(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		info, ok := h.lookup(w, id)
		if ok {
			writeSessionJSON(w, http.StatusOK, info)
		}
	case http.MethodDelete:
		if session, ok := h.registry.Get(id); ok {
			session.Stop()
			writeSessionJSON(w, http.StatusOK, session.Snapshot())
			return
		}
		// A persisted-only session is already terminal; stopping is a no-op.
		if info, ok := h.lookup(w, id); ok {
			writeSessionJSON(w, http.StatusOK, info)
		}
	default:
		h.next.ServeHTTP(w, r)
	}
}

// lookup resolves a session from the registry or the durable store, writing
// the HTTP error itself when the session is unknown.
func (h *sessionHandler) lookup(w http.ResponseWriter, id string) (query.SessionInfo, bool) {
	if session, ok := h.registry.Get(id); ok {
		return session.Snapshot(), true
	}
	if h.sessions != nil {
		info, ok, err := h.sessions.Get(context.Background(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return query.SessionInfo{}, false
		}
		if ok {
			return info, true
		}
	}
	http.Error(w, fmt.Sprintf("session %q not found", id), http.StatusNotFound)
	return query.SessionInfo{}, false
}

func (h *sessionHandler) events(w http.ResponseWriter, r *http.Request, id string) {
	after, err := sse.LastEventSequence(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if session, ok := h.registry.Get(id); ok {
		if r.URL.Query().Get("format") == "ndjson" {
			sse.WriteNDJSON(w, sessionEventsFilename(id), session.Events())
			return
		}
		sseStream{session: session, after: after, keepalive: sse.DefaultKeepalive}.run(w, r)
		return
	}

	info, ok := h.lookup(w, id)
	if !ok {
		return
	}
	events, err := h.sessions.Events(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.URL.Query().Get("format") == "ndjson" {
		sse.WriteNDJSON(w, sessionEventsFilename(id), events)
		return
	}
	sseHistory{events: events, info: info, after: after}.write(w)
}

func sessionEventsFilename(id string) string { return "session-" + id + ".ndjson" }

func (h *sessionHandler) result(w http.ResponseWriter, r *http.Request, id string) {
	var result *query.Result
	if session, ok := h.registry.Get(id); ok {
		var err error
		if result, err = session.Result(h.ctx); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		info, ok := h.lookup(w, id)
		if !ok {
			return
		}
		resolved, err := profiles.Resolve(r.Context(), h.store, info.Profile)
		if err != nil {
			http.Error(w, fmt.Sprintf("session %q: %v", id, err), http.StatusNotFound)
			return
		}
		events, err := h.sessions.Events(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if result, err = query.MaterializeEvents(h.ctx, resolved.Profile, events); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	rows := result.Rows
	if rows == nil {
		rows = []query.Row{}
	}
	writeSessionJSON(w, http.StatusOK, rows)
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
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
