package sessions

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/flanksource/commons-db/query"
)

func (h *sessionHandler) info(w http.ResponseWriter, r *http.Request, id string) {
	if resolved, ok := h.authorizedSession(w, r, id, ActionRead); ok {
		writeSessionJSON(w, http.StatusOK, resolved.Info)
	}
}

// liveForControl resolves a session a control action applies to: authorized
// for control, live in this registry and not ended. It writes the refusal
// itself.
func (h *sessionHandler) liveForControl(w http.ResponseWriter, r *http.Request, id, action string) (*query.Session, bool) {
	resolved, ok := h.authorizedSession(w, r, id, ActionControl)
	switch {
	case !ok:
		return nil, false
	case resolved.Live == nil:
		http.Error(w, fmt.Sprintf("%s session %s: %v (owner %s, pid %d)", action, id, query.ErrSessionNotLive,
			resolved.Info.Owner.Host, resolved.Info.Owner.PID), http.StatusConflict)
		return nil, false
	case resolved.Info.State.Terminal():
		http.Error(w, fmt.Sprintf("%s session %s: it is %s", action, id, resolved.Info.State), http.StatusConflict)
		return nil, false
	}
	return resolved.Live, true
}

func (h *sessionHandler) stop(w http.ResponseWriter, r *http.Request, id string) {
	session, ok := h.liveForControl(w, r, id, "stop")
	if !ok {
		return
	}
	reason := "stopped via API"
	if h.principal != nil {
		reason = "stopped by " + h.principal(r)
	}
	session.Stop(reason)
	h.writeLive(w, r, http.StatusOK, session)
}

// extend adds ?duration to the session's deadline; the registry clamps it to
// its maximum duration from the session's start.
func (h *sessionHandler) extend(w http.ResponseWriter, r *http.Request, id string) {
	duration, err := durationParam(r, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	session, ok := h.liveForControl(w, r, id, "extend")
	if !ok {
		return
	}
	info := session.Snapshot()
	stopAt := time.Now()
	if info.StopAt != nil {
		stopAt = *info.StopAt
	}
	if _, err := h.registry.Extend(id, stopAt.Add(duration)); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	h.writeLive(w, r, http.StatusOK, session)
}

// restart starts a new session from an ended, restartable one and answers 201
// with the new session.
func (h *sessionHandler) restart(w http.ResponseWriter, r *http.Request, id string) {
	duration, err := durationParam(r, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resolved, ok := h.authorizedSession(w, r, id, ActionControl)
	switch {
	case !ok:
		return
	case !resolved.Info.State.Terminal():
		http.Error(w, fmt.Sprintf("restart session %s: it is %s; only an ended session restarts", id, resolved.Info.State), http.StatusConflict)
		return
	case !resolved.Info.Restartable:
		http.Error(w, fmt.Sprintf("restart session %s (%s): %v", id, resolved.Info.Profile, query.ErrNotRestartable), http.StatusConflict)
		return
	}
	overrides := query.RestartOverrides{Duration: duration}
	if h.principal != nil {
		overrides.Principal = h.principal(r)
	}
	session, err := h.registry.Restart(h.sessionContext(r), id, overrides)
	switch {
	case errors.Is(err, query.ErrMaxSessions), errors.Is(err, query.ErrNotRestartable):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.writeLive(w, r, http.StatusCreated, session)
}

// durationParam reads ?duration as a positive Go duration; zero when absent
// and not required.
func durationParam(r *http.Request, required bool) (time.Duration, error) {
	raw := r.URL.Query().Get("duration")
	if raw == "" {
		if required {
			return 0, errors.New("duration is required, e.g. ?duration=15m")
		}
		return 0, nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("duration %q must be a positive Go duration, e.g. 15m", raw)
	}
	return duration, nil
}
