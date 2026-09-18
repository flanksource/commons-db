package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/flanksource/commons-db/query"
)

// Action is what a caller does with a session: read it, or control it.
type Action string

const (
	// ActionRead lists a session, or serves its info, events or result.
	ActionRead Action = "read"
	// ActionControl starts, stops, extends or restarts a session.
	ActionControl Action = "control"
)

// AuthorizeFunc decides whether r may perform action on a session of profile.
// A non-nil error refuses. On a route naming a session id, a caller who may not
// read the session is answered 404 exactly as for a session that does not
// exist; a caller who may read it but not control it gets 403 with the error.
// Where the caller named the profile itself, a refusal is 403 with the error.
type AuthorizeFunc func(r *http.Request, profile string, action Action) error

// authorized asks Options.Authorize whether r may perform action on a session
// of profile, answering a refusal 403 itself. It serves routes where the caller
// named the profile, so a refusal tells it nothing it did not already know.
func (h *sessionHandler) authorized(w http.ResponseWriter, r *http.Request, profile string, action Action) bool {
	if err := h.refusal(r, profile, action); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return false
	}
	return true
}

// readAllow is SessionFilter.Allow for r: Authorize for reading, asked once per
// profile for the whole request. Nil when every profile is allowed.
func (h *sessionHandler) readAllow(r *http.Request) func(profile string) bool {
	if h.authorize == nil {
		return nil
	}
	var mu sync.Mutex
	decided := map[string]bool{}
	return func(profile string) bool {
		mu.Lock()
		defer mu.Unlock()
		allowed, ok := decided[profile]
		if !ok {
			allowed = h.authorize(r, profile, ActionRead) == nil
			decided[profile] = allowed
		}
		return allowed
	}
}

// resolvedSession is a session found live in the registry or only in the
// store; Live is nil for the latter.
type resolvedSession struct {
	Live *query.Session
	Info query.SessionInfo
}

// authorizedSession resolves session id — live from the registry, or from the
// session store — and authorizes r for action on its profile before anything
// of it is used, with the info's overlays derived. It writes the response
// itself when the session is unknown, refused or cannot be read.
//
// A session r may not read is answered 404 with the body a missing session
// gets, so an id alone never tells a caller that a session it cannot see
// exists. Only a caller who may read it is told 403 for a control it may not
// perform.
func (h *sessionHandler) authorizedSession(w http.ResponseWriter, r *http.Request, id string, action Action) (resolvedSession, bool) {
	ctx := withSessionReadMemo(r.Context())
	resolved, err := h.resolve(ctx, id)
	var notFound errSessionNotFound
	switch {
	case errors.As(err, &notFound):
		http.Error(w, err.Error(), http.StatusNotFound)
		return resolved, false
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return resolved, false
	}
	if refusal := h.refusal(r, resolved.Info.Profile, action); refusal != nil {
		status, body := http.StatusNotFound, errSessionNotFound(id).Error()
		if action == ActionControl && h.authorize(r, resolved.Info.Profile, ActionRead) == nil {
			status, body = http.StatusForbidden, refusal.Error()
		}
		http.Error(w, body, status)
		return resolved, false
	}
	infos, err := h.overlay(ctx, []query.SessionRecord{resolved.Info.SessionRecord}, h.readAllow(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return resolved, false
	}
	resolved.Info = infos[0]
	return resolved, true
}

// refusal is Authorize's refusal of action on profile for r, or nil.
func (h *sessionHandler) refusal(r *http.Request, profile string, action Action) error {
	if h.authorize == nil {
		return nil
	}
	return h.authorize(r, profile, action)
}

type errSessionNotFound string

func (e errSessionNotFound) Error() string { return fmt.Sprintf("session %q not found", string(e)) }

func (h *sessionHandler) resolve(ctx context.Context, id string) (resolvedSession, error) {
	if session, ok := h.registry.Get(id); ok {
		return resolvedSession{Live: session, Info: session.Snapshot()}, nil
	}
	if h.sessions == nil {
		return resolvedSession{}, errSessionNotFound(id)
	}
	rec, found, err := h.sessions.Get(ctx, id)
	switch {
	case err != nil:
		return resolvedSession{}, err
	case !found:
		return resolvedSession{}, errSessionNotFound(id)
	}
	return resolvedSession{Info: query.SessionInfo{SessionRecord: rec}}, nil
}
