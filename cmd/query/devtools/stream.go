package devtools

// One stream carries both records and log lines.
//
// A console that opened two connections would spend two of the six an HTTP/1.1
// origin gets, and this server already holds session streams on the same origin.
// The frames are tagged, so a client that only wants one kind ignores the other.

import (
	"io"
	"net/http"
	"time"

	"github.com/flanksource/commons-db/cmd/query/internal/sse"
	"github.com/flanksource/commons-db/query"
)

type stream struct {
	records   *Store
	after     int64
	keepalive time.Duration

	// logsOnly serves the tail on its own, for a caller that wants the log
	// endpoint rather than the multiplexed one. The two sequence spaces are
	// separate, so a client must not mix them on one connection's Last-Event-ID.
	logsOnly bool
}

// run replays what the client has not seen, then follows until it disconnects.
//
// The keepalive is a case in this select rather than a goroutine on a timer:
// every byte of the stream is written from here, so a ping can never land
// between an event's id and its data.
func (s stream) run(w http.ResponseWriter, r *http.Request) {
	if s.logsOnly {
		s.runLogs(w, r)
		return
	}

	recordReplay, records, cancelRecords := s.records.SubscribeRecords(s.after)
	defer cancelRecords()
	logReplay, logs, cancelLogs := s.records.SubscribeLogs(0)
	defer cancelLogs()

	flusher := sse.Begin(w)
	for _, record := range recordReplay {
		if sse.WriteFrame(w, sse.Frame{Event: "record", ID: record.Sequence, Data: record}) != nil {
			return
		}
	}
	// Replayed log lines carry no frame id. Last-Event-ID resumes the record
	// stream, which is the one whose gaps matter; stamping log sequences onto the
	// same connection would make a reconnect resume the wrong sequence space.
	for _, line := range logReplay {
		if sse.WriteFrame(w, sse.Frame{Event: "log", Data: line}) != nil {
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
		case record, open := <-records:
			if !open {
				return
			}
			if sse.WriteFrame(w, sse.Frame{Event: "record", ID: record.Sequence, Data: record}) != nil {
				return
			}
			flusher.Flush()
		case line, open := <-logs:
			if !open {
				return
			}
			if sse.WriteFrame(w, sse.Frame{Event: "log", Data: line}) != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s stream) runLogs(w http.ResponseWriter, r *http.Request) {
	replay, live, cancel := s.records.SubscribeLogs(s.after)
	defer cancel()

	flusher := sse.Begin(w)
	for _, line := range replay {
		if sse.WriteFrame(w, logFrame(line)) != nil {
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
		case line, open := <-live:
			if !open {
				return
			}
			if sse.WriteFrame(w, logFrame(line)) != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func logFrame(line query.LogLine) sse.Frame {
	return sse.Frame{Event: "log", ID: line.Sequence, Data: line}
}
