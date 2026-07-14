package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

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
//	POST   {prefix}/profile/{name}/sessions   start (?interval samples any profile)
//	GET    {prefix}/sessions                  list (live ∪ persisted)
//	GET    {prefix}/sessions/{id}             info
//	DELETE {prefix}/sessions/{id}             stop
//	GET    {prefix}/sessions/{id}/events      SSE stream (?format=ndjson exports)
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
	p, err := h.store.Get(r.Context(), name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if p, err = applySessionSpecOverrides(p, r.URL.Query().Get("interval"), r.URL.Query().Get("duration")); err != nil {
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

// applySessionSpecOverrides maps the transport inputs (HTTP query params or CLI
// flags) onto the profile: interval samples any plain profile as top (or
// overrides a declared interval), and duration lowers the session bound (the
// registry still clamps it).
func applySessionSpecOverrides(p query.Profile, interval, duration string) (query.Profile, error) {
	if interval != "" {
		d, err := time.ParseDuration(interval)
		if err != nil {
			return p, fmt.Errorf("invalid interval %q: %w", interval, err)
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
		return p, fmt.Errorf("profile %q declares neither trace nor top; pass ?interval to sample it", p.Name)
	}
	if duration != "" {
		d, err := time.ParseDuration(duration)
		if err != nil {
			return p, fmt.Errorf("invalid duration %q: %w", duration, err)
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
	if session, ok := h.registry.Get(id); ok {
		if r.URL.Query().Get("format") == "ndjson" {
			writeNDJSON(w, id, session.Events())
			return
		}
		h.streamSSE(w, r, session)
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
		writeNDJSON(w, id, events)
		return
	}
	// Persisted sessions are terminal: replay then done.
	flusher := beginSSE(w)
	for _, e := range events {
		if writeSSEEvent(w, "event", e) != nil {
			return
		}
	}
	_ = writeSSEEvent(w, "done", info)
	flusher.Flush()
}

// streamSSE replays the buffered events then follows the live channel until
// the session ends or the client disconnects.
func (h *sessionHandler) streamSSE(w http.ResponseWriter, r *http.Request, session *query.Session) {
	replay, live, cancel := session.Subscribe()
	defer cancel()

	flusher := beginSSE(w)
	for _, e := range replay {
		if writeSSEEvent(w, "event", e) != nil {
			return
		}
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-live:
			if !open {
				_ = writeSSEEvent(w, "done", session.Snapshot())
				flusher.Flush()
				return
			}
			if writeSSEEvent(w, "event", e) != nil {
				return
			}
			flusher.Flush()
		}
	}
}

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
		p, err := h.store.Get(r.Context(), info.Profile)
		if err != nil {
			http.Error(w, fmt.Sprintf("session %q: %v", id, err), http.StatusNotFound)
			return
		}
		events, err := h.sessions.Events(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if result, err = query.MaterializeEvents(h.ctx, p, events); err != nil {
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

// sessionReservedParam extends reservedParam with the session transport keys.
func sessionReservedParam(key string) bool {
	return profiles.IsReservedParam(key) || key == "interval" || key == "duration"
}

func writeSessionJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func beginSSE(w http.ResponseWriter) http.Flusher {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher, ok := w.(http.Flusher)
	if !ok {
		panic("session SSE requires a flushable ResponseWriter")
	}
	return flusher
}

// writeSSEEvent writes one SSE frame; an error means the client disconnected
// and the stream should stop.
func writeSSEEvent(w http.ResponseWriter, event string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		data = []byte(fmt.Sprintf(`{"error":%q}`, err.Error()))
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	return err
}

func writeNDJSON(w http.ResponseWriter, id string, events []query.Event) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "session-"+id+".ndjson"))
	enc := json.NewEncoder(w)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return
		}
	}
}
