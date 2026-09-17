package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/flanksource/commons/logger"

	"github.com/flanksource/commons-db/cmd/query/internal/sse"
	"github.com/flanksource/commons-db/cmd/query/profiles"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// events serves a session's events: from the record stream its status names
// (followed while this process still writes it), from a live stream session's
// ring, or from the event log a finished stream session was persisted to.
func (h *sessionHandler) events(w http.ResponseWriter, r *http.Request, id string) {
	after, err := sse.LastEventSequence(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resolved, ok := h.authorizedSession(w, r, id, ActionRead)
	if !ok {
		return
	}
	info, ndjson := resolved.Info, r.URL.Query().Get("format") == "ndjson"
	switch {
	case info.Events != nil:
		h.replayRecords(w, r, resolved, after, ndjson)
	case resolved.Live != nil && info.Kind != query.KindCapture && ndjson:
		sse.WriteNDJSON(w, sessionEventsFilename(id), resolved.Live.Events())
	case resolved.Live != nil && info.Kind != query.KindCapture:
		sseStream{session: resolved.Live, after: after, keepalive: sse.DefaultKeepalive}.run(w, r)
	case h.eventLog != nil && info.Kind != query.KindCapture:
		events, err := h.eventLog.Events(r.Context(), id)
		switch {
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		case ndjson:
			sse.WriteNDJSON(w, sessionEventsFilename(id), events)
		default:
			sseHistory{events: events, info: info, after: after}.write(w)
		}
	default:
		http.Error(w, fmt.Sprintf("session %s recorded no events", id), http.StatusNotFound)
	}
}

func sessionEventsFilename(id string) string { return "session-" + id + ".ndjson" }

// goneResponse is the 410 body for a session whose record stream no longer
// exists: where it was, so an operator knows what was lost.
type goneResponse struct {
	Error string                    `json:"error"`
	Store query.EventsStoreLocation `json:"store"`
}

// replayRecords serves the session's recorded window, following the stream
// past it while this process writes the still-active session. A stream that no
// longer exists, or exists as another generation than the ref's — its id was
// reused after the recorded one expired — is gone: 410 with where it was.
func (h *sessionHandler) replayRecords(w http.ResponseWriter, r *http.Request, resolved resolvedSession, after int64, ndjson bool) {
	info := resolved.Info
	ref := info.Events
	if h.records == nil {
		http.Error(w, fmt.Sprintf("session %s: its events are in record stream %q, but this server has no record store", info.ID, ref.Stream), http.StatusInternalServerError)
		return
	}
	meta, err := h.records.Meta(r.Context(), ref.Stream)
	switch {
	case errors.Is(err, recordstore.ErrNotFound):
		writeSessionJSON(w, http.StatusGone, goneResponse{
			Error: fmt.Sprintf("session %s: record stream %q is gone: %v", info.ID, ref.Stream, recordstore.ErrNotFound),
			Store: ref.Store,
		})
		return
	case err != nil:
		http.Error(w, fmt.Sprintf("session %s: read record stream %q: %v", info.ID, ref.Stream, err), http.StatusInternalServerError)
		return
	case meta.Generation != ref.Generation:
		writeSessionJSON(w, http.StatusGone, goneResponse{
			Error: fmt.Sprintf("session %s: record stream %q generation %q is gone; the stream now holds generation %q",
				info.ID, ref.Stream, ref.Generation, meta.Generation),
			Store: ref.Store,
		})
		return
	}
	window := recordWindow{ref: *ref, after: max(after, ref.From-1)}
	if ndjson {
		h.exportRecords(w, r, info.ID, window)
		return
	}
	h.streamRecords(w, r, resolved, window)
}

// streamRecords writes the window as SSE, then a done frame carrying the
// session as it stands, or an error frame when the stream failed mid-way. A
// session this process still writes is followed until its stream is sealed or
// the session ends, whichever comes first.
func (h *sessionHandler) streamRecords(w http.ResponseWriter, r *http.Request, resolved resolvedSession, window recordWindow) {
	info := resolved.Info
	flusher, err := sse.Begin(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stream := recordStream{records: h.records, id: info.ID, window: window, w: w, flusher: flusher}
	if info.LocalWriter && !info.State.Terminal() {
		err = stream.follow(r.Context(), sse.DefaultKeepalive, sessionEnded(resolved.Live))
	} else {
		err = stream.scan(r.Context())
	}
	var final resolvedSession
	if err == nil {
		final, err = h.resolve(r.Context(), info.ID)
	}
	if err != nil {
		_ = sse.WriteFrame(w, sse.Frame{Event: "error", Data: map[string]string{"error": err.Error()}})
		flusher.Flush()
		return
	}
	info.SessionRecord = final.Info.SessionRecord
	_ = sse.WriteFrame(w, sse.Frame{Event: "done", Data: info})
	flusher.Flush()
}

// exportRecords streams the window as NDJSON, one line per row as the scan
// reads it. A scan failing before its first row is a 500; one failing after
// the file began aborts the response, so the client sees a broken transfer
// rather than a short file that looks complete.
func (h *sessionHandler) exportRecords(w http.ResponseWriter, r *http.Request, id string, window recordWindow) {
	var encoder *json.Encoder
	err := h.records.Scan(r.Context(), window.ref.Stream, window.after, func(seq int64, row recordstore.Row) error {
		if window.past(seq) {
			return errWindowEnd
		}
		if encoder == nil {
			encoder = sse.BeginNDJSON(w, sessionEventsFilename(id))
		}
		return encoder.Encode(query.Event{SessionID: id, Sequence: seq, Row: row})
	})
	switch {
	case (err == nil || errors.Is(err, errWindowEnd)) && encoder == nil:
		sse.BeginNDJSON(w, sessionEventsFilename(id))
	case err == nil || errors.Is(err, errWindowEnd):
	case encoder == nil:
		http.Error(w, fmt.Sprintf("session %s: read record stream %q: %v", id, window.ref.Stream, err), http.StatusInternalServerError)
	default:
		logger.Errorf("session %s: export of record stream %q failed mid-file: %v", id, window.ref.Stream, err)
		panic(http.ErrAbortHandler)
	}
}

// sessionEnded is closed once live ends. A record this process writes through
// another registry has no session here to watch: its follow ends when the
// stream is sealed or the client leaves, and the nil channel never fires.
func sessionEnded(live *query.Session) <-chan struct{} {
	if live == nil {
		return nil
	}
	return live.Done()
}

// errWindowEnd stops a scan at the end of the recorded window.
var errWindowEnd = errors.New("end of the recorded window")

// recordWindow is the part of a record stream a session's ref names, after
// what the client already holds.
type recordWindow struct {
	ref   query.EventsRef
	after int64
}

// past reports whether seq lies beyond the window's recorded end.
func (w recordWindow) past(seq int64) bool { return w.ref.To > 0 && seq > w.ref.To }

// recordStream writes a record stream's rows as SSE event frames.
type recordStream struct {
	records EventRecords
	id      string
	window  recordWindow
	w       http.ResponseWriter
	flusher http.Flusher
}

func (s recordStream) write(seq int64, row recordstore.Row) error {
	if err := sse.WriteFrame(s.w, sse.Frame{Event: "event", ID: seq, Data: query.Event{SessionID: s.id, Sequence: seq, Row: row}}); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s recordStream) scan(ctx context.Context) error {
	err := s.records.Scan(ctx, s.window.ref.Stream, s.window.after, func(seq int64, row recordstore.Row) error {
		if s.window.past(seq) {
			return errWindowEnd
		}
		return s.write(seq, row)
	})
	if errors.Is(err, errWindowEnd) {
		return nil
	}
	return err
}

type tailedRow struct {
	seq int64
	row recordstore.Row
}

// follow tails the stream until it is sealed, the client leaves, or ended
// closes — a session that stopped, timed out or aborted without sealing its
// stream — after which it drains the rows up to the stream's high seq. Every
// byte is written from this goroutine, so a keepalive never splits a frame.
func (s recordStream) follow(ctx context.Context, keepalive time.Duration, ended <-chan struct{}) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rows, done := make(chan tailedRow), make(chan error, 1)
	go func() {
		done <- s.records.Tail(ctx, s.window.ref.Stream, s.window.after, func(seq int64, row recordstore.Row) error {
			select {
			case rows <- tailedRow{seq: seq, row: row}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	ticker := time.NewTicker(keepalive)
	defer ticker.Stop()
	last, drainTo := s.window.after, int64(-1)
	for drainTo < 0 || last < drainTo {
		select {
		case err := <-done:
			return err
		case <-ended:
			meta, err := s.records.Meta(ctx, s.window.ref.Stream)
			if err != nil {
				return fmt.Errorf("session %s ended: read record stream %q: %w", s.id, s.window.ref.Stream, err)
			}
			drainTo, ended = meta.HighSeq, nil
		case <-ticker.C:
			if _, err := s.w.Write([]byte(sse.KeepaliveFrame)); err != nil {
				return err
			}
			s.flusher.Flush()
		case tailed := <-rows:
			if err := s.write(tailed.seq, tailed.row); err != nil {
				return err
			}
			last = tailed.seq
		}
	}
	return nil
}

// result serves the result a capture recorded in its status, or a stream
// session's materialized rows.
func (h *sessionHandler) result(w http.ResponseWriter, r *http.Request, id string) {
	resolved, ok := h.authorizedSession(w, r, id, ActionRead)
	if !ok {
		return
	}
	info := resolved.Info
	switch {
	case len(info.Result) > 0:
		writeSessionJSON(w, http.StatusOK, info.Result)
	case info.Kind == query.KindCapture:
		http.Error(w, fmt.Sprintf("session %s recorded no result", id), http.StatusNotFound)
	case resolved.Live != nil:
		result, err := resolved.Live.Result(h.ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeRows(w, result)
	case h.eventLog != nil:
		h.materializeLogged(w, r, info)
	default:
		http.Error(w, fmt.Sprintf("session %s recorded no result", id), http.StatusNotFound)
	}
}

func (h *sessionHandler) materializeLogged(w http.ResponseWriter, r *http.Request, info query.SessionInfo) {
	resolved, err := profiles.Resolve(r.Context(), h.store, info.Profile)
	if err != nil {
		http.Error(w, fmt.Sprintf("session %q: %v", info.ID, err), http.StatusNotFound)
		return
	}
	events, err := h.eventLog.Events(r.Context(), info.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	result, err := query.MaterializeEvents(h.ctx, resolved.Profile, events)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeRows(w, result)
}

func writeRows(w http.ResponseWriter, result *query.Result) {
	rows := result.Rows
	if rows == nil {
		rows = []query.Row{}
	}
	writeSessionJSON(w, http.StatusOK, rows)
}
