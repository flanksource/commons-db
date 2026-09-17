package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/types"
)

func (h *sessionHandler) start(w http.ResponseWriter, r *http.Request, name string) {
	// A surface key is the path the OpenAPI document hands out for a profile's
	// session start, and the only one a name holding a "/" can take.
	stored, err := profiles.StoredProfileName(r.Context(), h.store, name)
	switch {
	case errors.Is(err, profiles.ErrProfileSurfaceNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !h.authorized(w, r, stored, ActionControl) {
		return
	}
	resolved, err := profiles.Resolve(r.Context(), h.store, stored)
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

	session, err := query.ExecuteStream(h.sessionContext(r), h.registry, p, params)
	if err != nil {
		http.Error(w, err.Error(), sessionStartErrorStatus(err))
		return
	}
	h.writeLive(w, r, http.StatusCreated, session)
}

func sessionStartErrorStatus(err error) int {
	switch {
	case errors.Is(err, query.ErrMaxSessions):
		return http.StatusConflict
	case errors.Is(err, query.ErrPrepareRead):
		// The registry's BeforeRead failed: answered by its cause, exactly as
		// the profile service answers its own hook.
		status, _ := profiles.PrepareErrorStatus(err)
		return status
	}
	return http.StatusBadRequest
}

// writeLive answers with a live session's info and its overlays.
func (h *sessionHandler) writeLive(w http.ResponseWriter, r *http.Request, status int, session *query.Session) {
	infos, err := h.overlay(r.Context(), []query.SessionRecord{session.Snapshot().SessionRecord}, h.readAllow(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeSessionJSON(w, status, infos[0])
}

// sessionContext is the context a session r starts runs under, and the one its
// registry's BeforeRead prepares the read with. It carries r's values — the
// tenant or environment a routed record store reads from the context — but not
// r's cancellation, because a session outlives the request that started it; its
// own duration bound and Stop end it. The server context's capabilities
// (connection resolver, logger, tracer, namespace, DB) are laid over them by
// Wrap, as every profile read does.
func (h *sessionHandler) sessionContext(r *http.Request) dbcontext.Context {
	return h.ctx.Wrap(context.WithoutCancel(r.Context()))
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
