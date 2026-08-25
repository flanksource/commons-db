// Package sse holds the server-sent-events wire format shared by every stream
// this server serves — session events and the devtools console alike.
//
// It is deliberately only the wire: framing, the resume header, and the
// flush-and-keepalive contract. What to stream, and when a stream is over, is
// the caller's business.
package sse

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultKeepalive is how often an otherwise silent stream reassures the
// proxies between here and the browser that the connection is alive. A trace
// that ran for seconds never needed one; a tail can sit idle for minutes, and
// the common idle timeout in front of it is a minute.
const DefaultKeepalive = 20 * time.Second

// KeepaliveFrame is a comment frame: the one thing that can be written into an
// event stream without being an event.
//
// Write it from the same goroutine — ideally the same select — that writes
// event frames. A ping emitted from a timer goroutine can land between a
// frame's id and its data, which splits the frame and desynchronises the
// client for the life of the connection.
const KeepaliveFrame = ": ping\n\n"

// Frame is one wire frame. ID carries the event's sequence, which is what a
// reconnecting browser echoes back in Last-Event-ID — a frame without it is a
// frame the client cannot resume from, so only a terminal frame (which nobody
// resumes into) leaves it unset.
type Frame struct {
	Event string
	ID    int64
	Data  any
}

// Begin writes the event-stream headers and returns the flusher the stream
// must call after every frame.
//
// It panics rather than degrading when the writer cannot flush: an unflushed
// event stream looks like a hung request, which is far harder to diagnose than
// a stack trace naming the middleware that swallowed the Flusher.
func Begin(w http.ResponseWriter) http.Flusher {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	flusher, ok := w.(http.Flusher)
	if !ok {
		panic("server-sent events require a flushable ResponseWriter")
	}
	return flusher
}

// WriteFrame writes one frame; an error means the client disconnected and the
// stream should stop.
func WriteFrame(w io.Writer, frame Frame) error {
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

// LastEventSequence reads the resume header a browser sends after a dropped
// connection. Anything that is not a sequence is a client the server cannot
// answer correctly — replaying the whole ring instead would silently duplicate
// every event it already has.
func LastEventSequence(r *http.Request) (int64, error) {
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

// WriteNDJSON serves the same events as a downloadable file, for a caller that
// wants the stream's contents without following it.
func WriteNDJSON[T any](w http.ResponseWriter, filename string, items []T) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	encoder := json.NewEncoder(w)
	for _, item := range items {
		if err := encoder.Encode(item); err != nil {
			return
		}
	}
}
