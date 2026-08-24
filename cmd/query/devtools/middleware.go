package devtools

// The arming middleware. One place mints the record, stamps the response and
// files the result, so a handler that wants to be explained only has to lift the
// recorder onto the context its executions run under.

import (
	stdcontext "context"
	"net/http"
	"strings"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

type recorderKey struct{}

// RecorderFromRequest returns the recorder this request was armed with, or nil.
//
// Handlers need this because the execution context is derived from the server's
// context rather than the request's — only cancellation is bridged — so a
// recorder cannot ride down on request-context values alone. Lifting it is one
// explicit line at each entry point, which is better than a hidden channel that
// works on three paths and silently does not on a fourth.
func RecorderFromRequest(r *http.Request) *query.Recorder {
	recorder, _ := r.Context().Value(recorderKey{}).(*query.Recorder)
	return recorder
}

// RequestWithRecorder is how a request becomes armed. The middleware calls it,
// and so does any test that needs to exercise a handler's armed path without
// standing up the whole chain in front of it.
func RequestWithRecorder(r *http.Request, recorder *query.Recorder) *http.Request {
	if recorder == nil {
		return r
	}
	return r.WithContext(stdcontext.WithValue(r.Context(), recorderKey{}, recorder))
}

// WithRequestRecorder is the lift itself: the context an entry point is about to
// execute on, carrying the recorder its request was armed with.
//
// An unarmed request gets its context back untouched rather than one carrying a
// nil recorder, so nothing downstream can tell an armed-at-nothing run from an
// ordinary one — there is no such state.
func WithRequestRecorder(ctx dbcontext.Context, r *http.Request) dbcontext.Context {
	recorder := RecorderFromRequest(r)
	if recorder == nil {
		return ctx
	}
	return query.WithRecorder(ctx, recorder)
}

// armableSurface names the request as one this middleware understands, or
// reports that it is not armable at all.
//
// Arming is allow-listed rather than universal: a record whose surface is unset
// would mean the middleware armed something it cannot describe, and a mystery
// row in a console is worse than no row.
func armableSurface(prefix string, r *http.Request) (surface string, profile string, ok bool) {
	path := strings.TrimPrefix(r.URL.Path, prefix)
	switch {
	case path == "/profile/sample/filters/values":
		return "sample", "", true
	case path == "/profile/sample":
		return "sample", "", true
	case strings.HasPrefix(path, "/profile/"):
		name := strings.TrimPrefix(path, "/profile/")
		// A sub-resource of a profile is not the profile's execution.
		if name == "" || strings.Contains(name, "/") {
			return "", "", false
		}
		return "profile", name, true
	case strings.HasPrefix(path, "/connection/") &&
		(strings.HasSuffix(path, "/browser/query") || strings.HasSuffix(path, "/browser/filters/values")):
		return "browser", "", true
	case strings.HasPrefix(path, "/reconcile"):
		return "reconcile", "", true
	default:
		return "", "", false
	}
}

// statusWriter remembers the status a handler wrote, which is the one fact about
// the response the middleware needs and the only one it can observe.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// arm wraps one request. It returns the request and writer the rest of the chain
// should see, plus a function that files the record once the handler returns.
func (h *Handler) arm(w http.ResponseWriter, r *http.Request) (http.ResponseWriter, *http.Request, func(), bool) {
	surface, profile, ok := armableSurface(h.prefix, r)
	if !ok {
		return w, r, nil, true
	}
	recorder, err := NewRequestRecorder(ArmOptions{
		Request: r,
		Source:  query.ExecutionSource{Surface: surface, Profile: profile},
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return w, r, nil, false
	}
	if recorder == nil {
		return w, r, nil, true
	}

	tracked := &statusWriter{ResponseWriter: StampID(w, recorder.ID())}
	armed := RequestWithRecorder(r, recorder)
	return tracked, armed, func() {
		status := tracked.status
		if status == 0 {
			status = http.StatusOK
		}
		recorder.Finish(query.FinishOptions{Status: status})
		h.store.Add(recorder)
	}, true
}
