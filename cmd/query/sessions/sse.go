package sessions

import (
	"io"
	"net/http"
	"time"

	"github.com/flanksource/commons-db/cmd/query/internal/sse"
	"github.com/flanksource/commons-db/query"
)

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

	flusher := sse.Begin(w)
	for _, e := range replay {
		if sse.WriteFrame(w, sse.Frame{Event: "event", ID: e.Sequence, Data: e}) != nil {
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
			if _, err := io.WriteString(w, sse.KeepaliveFrame); err != nil {
				return
			}
			flusher.Flush()
		case e, open := <-live:
			if !open {
				_ = sse.WriteFrame(w, sse.Frame{Event: "done", Data: s.session.Snapshot()})
				flusher.Flush()
				return
			}
			if sse.WriteFrame(w, sse.Frame{Event: "event", ID: e.Sequence, Data: e}) != nil {
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
	flusher := sse.Begin(w)
	for _, e := range h.events {
		if e.Sequence <= h.after {
			continue
		}
		if sse.WriteFrame(w, sse.Frame{Event: "event", ID: e.Sequence, Data: e}) != nil {
			return
		}
	}
	_ = sse.WriteFrame(w, sse.Frame{Event: "done", Data: h.info})
	flusher.Flush()
}
