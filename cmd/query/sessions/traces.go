// The trace plugin routes: list the trace kinds a server serves, and start a
// capture of one as a session the rest of the session API controls.

package sessions

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/flanksource/commons-db/tracing/traces"
)

// traceStartInput is the body of a trace start: the kind's params, and how
// long the capture runs (the registry's longest duration when empty).
type traceStartInput struct {
	Params   json.RawMessage `json:"params,omitempty"`
	Duration string          `json:"duration,omitempty"`
}

// traceKinds answers the kinds the caller may read, each with its params form
// and its columns.
func (h *sessionHandler) traceKinds(w http.ResponseWriter, r *http.Request) {
	kinds, err := h.traces.Kinds.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	allowed := make([]traces.KindInfo, 0, len(kinds))
	for _, kind := range kinds {
		if h.refusal(r, traces.ProfilePrefix+kind.Name, ActionRead) == nil {
			allowed = append(allowed, kind)
		}
	}
	writeSessionJSON(w, http.StatusOK, allowed)
}

// startTrace starts a capture of kind, authorized as its profile traces/<kind>.
func (h *sessionHandler) startTrace(w http.ResponseWriter, r *http.Request, kind string) {
	if !h.authorized(w, r, traces.ProfilePrefix+kind, ActionControl) {
		return
	}
	var input traceStartInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	request := traces.StartRequest{Kind: kind, Params: input.Params}
	if input.Duration != "" {
		duration, err := time.ParseDuration(input.Duration)
		if err != nil || duration <= 0 {
			http.Error(w, "duration must be positive (for example 5m)", http.StatusBadRequest)
			return
		}
		stopAt := time.Now().Add(duration)
		request.StopAt = &stopAt
	}
	if h.principal != nil {
		request.Principal = h.principal(r)
	}
	managed, err := h.traces.Start(h.sessionContext(r), request)
	switch {
	case errors.Is(err, traces.ErrUnknownKind):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, err.Error(), sessionStartErrorStatus(err))
		return
	}
	h.writeLive(w, r, http.StatusCreated, managed.Session())
}
