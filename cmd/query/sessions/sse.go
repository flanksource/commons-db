package sessions

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/commons-db/query"
)

// defaultSSEKeepalive is how often an otherwise silent stream reassures the
// proxies between here and the browser that the connection is alive. A trace
// that ran for seconds never needed one; a tail can sit idle for minutes, and
// the common idle timeout in front of it is a minute.
const defaultSSEKeepalive = 20 * time.Second

// sseKeepaliveFrame is a comment frame: the one thing that can be written into
// an event stream without being an event.
const sseKeepaliveFrame = ": ping\n\n"

// sseFrame is one wire frame. ID carries the event's sequence, which is what a
// reconnecting browser echoes back in Last-Event-ID — a frame without it is a
// frame the client cannot resume from, so only the terminal `done` frame (which
// nobody resumes into) leaves it unset.
type sseFrame struct {
	Event string
	ID    int64
	Data  any
}

// sseStream is one client's view of a live session: where its replay begins — a
// resuming client already holds everything up to After — and how often an idle
// stream pings.
type sseStream struct {
	session   *query.Session
	after     int64
	keepalive time.Duration
}

// run replays the buffered events the client has not seen, then follows the
// live channel until the session ends or the client disconnects.
//
// The keepalive is a case in this select rather than its own goroutine on a
// timer: every byte of the stream is written from here, so a ping can never
// land between the id and the data of an event frame.
func (s sseStream) run(w http.ResponseWriter, r *http.Request) {
	replay, live, cancel := s.session.SubscribeFrom(s.after)
	defer cancel()

	flusher := beginSSE(w)
	for _, e := range replay {
		if writeSSEFrame(w, sseFrame{Event: "event", ID: e.Sequence, Data: e}) != nil {
			return
		}
	}
	flusher.Flush()

	ticker := time.NewTicker(s.keepalive)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := io.WriteString(w, sseKeepaliveFrame); err != nil {
				return
			}
			flusher.Flush()
		case e, open := <-live:
			if !open {
				_ = writeSSEFrame(w, sseFrame{Event: "done", Data: s.session.Snapshot()})
				flusher.Flush()
				return
			}
			if writeSSEFrame(w, sseFrame{Event: "event", ID: e.Sequence, Data: e}) != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// sseHistory serves a session that is already over. There is nothing to wait
// for, so it neither subscribes nor keeps the connection alive: replay, then
// done.
type sseHistory struct {
	events []query.Event
	info   query.SessionInfo
	after  int64
}

func (h sseHistory) write(w http.ResponseWriter) {
	flusher := beginSSE(w)
	for _, e := range h.events {
		if e.Sequence <= h.after {
			continue
		}
		if writeSSEFrame(w, sseFrame{Event: "event", ID: e.Sequence, Data: e}) != nil {
			return
		}
	}
	_ = writeSSEFrame(w, sseFrame{Event: "done", Data: h.info})
	flusher.Flush()
}

// lastEventSequence reads the resume header a browser sends after a dropped
// connection. Anything that is not a sequence is a client the server cannot
// answer correctly — replaying the whole ring instead would silently duplicate
// every event it already has.
func lastEventSequence(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		return 0, nil
	}
	sequence, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || sequence < 0 {
		return 0, fmt.Errorf("invalid Last-Event-ID %q: expected an event sequence", raw)
	}
	return sequence, nil
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

// writeSSEFrame writes one frame; an error means the client disconnected and
// the stream should stop.
func writeSSEFrame(w io.Writer, frame sseFrame) error {
	data, err := json.Marshal(frame.Data)
	if err != nil {
		data = []byte(fmt.Sprintf(`{"error":%q}`, err.Error()))
	}
	var out strings.Builder
	if frame.ID > 0 {
		fmt.Fprintf(&out, "id: %d\n", frame.ID)
	}
	fmt.Fprintf(&out, "event: %s\ndata: %s\n\n", frame.Event, data)
	_, err = io.WriteString(w, out.String())
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
