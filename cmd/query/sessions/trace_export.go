// The trace export route: a window of a capture's records downloaded in one of
// its kind's formats.

package sessions

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/tracing"
)

// exportTrace downloads the window ?stream, ?afterSeq, ?toSeq of kind in
// ?format, authorized as reading its profile traces/<kind>. A refusal found
// before the first byte is answered with its status; a failure once the
// download has begun aborts it, so a client never keeps a file cut short.
func (h *sessionHandler) exportTrace(w http.ResponseWriter, r *http.Request, kind string) {
	query := r.URL.Query()
	format, err := h.traces.Kinds.Format(kind, query.Get("format"))
	if err != nil {
		http.Error(w, err.Error(), traceExportErrorStatus(err))
		return
	}
	if !h.authorized(w, r, tracing.ProfilePrefix+kind, ActionRead) {
		return
	}
	ref, err := traceRef(kind, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	download := &attachment{w: w, format: format, filename: fmt.Sprintf("%s-%s.%s", kind, ref.Stream, format.Ext)}
	err = h.traces.Export(h.ctx.Wrap(r.Context()), ref, format.Name, download)
	switch {
	case err != nil && download.started:
		panic(http.ErrAbortHandler)
	case err != nil:
		http.Error(w, err.Error(), traceExportErrorStatus(err))
	default:
		// An empty export is still answered as a download; an empty write cannot fail.
		_, _ = download.Write(nil)
	}
}

// traceRef is the window query names of kind's stream.
func traceRef(kind string, query url.Values) (tracing.TraceRef, error) {
	ref := tracing.TraceRef{Kind: kind, Stream: query.Get("stream")}
	if ref.Stream == "" {
		return ref, errors.New("stream is required")
	}
	for name, seq := range map[string]*int64{"afterSeq": &ref.AfterSeq, "toSeq": &ref.ToSeq} {
		if raw := query.Get(name); raw != "" {
			parsed, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return ref, fmt.Errorf("%s %q is not a seq", name, raw)
			}
			*seq = parsed
		}
	}
	return ref, ref.Validate()
}

func traceExportErrorStatus(err error) int {
	switch {
	case errors.Is(err, tracing.ErrUnknownKind), errors.Is(err, recordstore.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, tracing.ErrUnknownFormat), errors.Is(err, tracing.ErrInvalidRef):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// attachment writes a download's headers with its first byte, so an export
// refused before it writes anything is still answered with its error.
type attachment struct {
	w        http.ResponseWriter
	format   tracing.ExportFormat
	filename string
	started  bool
}

// Write sets the download headers on the same writer it writes the body to, so
// the non-HTML content type is visibly bound to the untrusted bytes it carries.
func (a *attachment) Write(p []byte) (int, error) {
	w := a.w
	if !a.started {
		a.started = true
		header := w.Header()
		// A plugin's format must not turn captured bytes into an active browser document.
		switch a.format.MIME {
		case "application/x-ndjson":
			header.Set("Content-Type", "application/x-ndjson")
		case "application/json":
			header.Set("Content-Type", "application/json")
		case "application/x-asciicast":
			header.Set("Content-Type", "application/x-asciicast")
		default:
			header.Set("Content-Type", "application/octet-stream")
		}
		header.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", a.filename))
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Content-Security-Policy", "sandbox")
		w.WriteHeader(http.StatusOK)
	}
	if len(p) == 0 {
		return 0, nil
	}
	return w.Write(p)
}
