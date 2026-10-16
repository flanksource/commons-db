// The trace plugin routes: list the trace kinds a server serves, and start a
// capture of one as a session the rest of the session API controls.

package sessions

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/flanksource/commons-db/tracing"
)

// traceStartInput is the body of a trace start: the kind's params, how long
// the capture runs (the registry's longest duration when empty), and labels a
// host lists its sessions by.
type traceStartInput struct {
	Params   json.RawMessage   `json:"params,omitempty"`
	Duration string            `json:"duration,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// traceKinds answers the kinds the caller may read, each with its params form
// and its columns.
func (h *sessionHandler) traceKinds(w http.ResponseWriter, r *http.Request) {
	kinds, err := h.traces.Kinds.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	allowed := make([]tracing.KindInfo, 0, len(kinds))
	for _, kind := range kinds {
		if h.refusal(r, tracing.ProfilePrefix+kind.Name, ActionRead) == nil {
			allowed = append(allowed, kind)
		}
	}
	writeSessionJSON(w, http.StatusOK, allowed)
}

// startTrace starts a capture of kind, authorized as its profile traces/<kind>.
func (h *sessionHandler) startTrace(w http.ResponseWriter, r *http.Request, kind string) {
	if !h.authorized(w, r, tracing.ProfilePrefix+kind, ActionControl) {
		return
	}
	var input traceStartInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	request := tracing.StartRequest{Kind: kind, Params: input.Params, Labels: input.Labels}
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
	case errors.Is(err, tracing.ErrUnknownKind):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case err != nil && h.traceStartError != nil && h.traceStartError(w, r, err):
		return
	case err != nil:
		http.Error(w, err.Error(), sessionStartErrorStatus(err))
		return
	}
	h.writeLive(w, r, http.StatusCreated, managed.Session())
}
