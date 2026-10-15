package pgproxy

import (
	"encoding/binary"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/flanksource/commons-db/connection"
)

// maxWireMessage bounds a single protocol message the sniffer will buffer. A
// COPY payload or a multi-megabyte bytea parameter is legitimate traffic that
// nobody wants a copy of in memory, so oversized messages are skipped in the
// sniffer while still being forwarded byte-for-byte by the proxy.
const maxWireMessage = 1 << 20

// pgSniffer frames one direction of a postgres connection. It is a Writer so it
// can sit in an io.MultiWriter beside the real destination: the proxy forwards
// raw bytes and the sniffer only ever sees a copy. Re-serialising parsed
// messages instead would corrupt extended-query, COPY and pipelined traffic in
// ways that present as flaky tests.
type pgSniffer struct {
	onMessage func(typ byte, body []byte)

	buf  []byte
	skip int64 // bytes remaining of an oversized message being discarded
}

func (s *pgSniffer) Write(p []byte) (int, error) {
	written := len(p)

	if s.skip > 0 {
		drop := min(s.skip, int64(len(p)))
		s.skip -= drop
		p = p[drop:]
	}
	s.buf = append(s.buf, p...)

	for len(s.buf) >= 5 {
		length := int64(binary.BigEndian.Uint32(s.buf[1:5]))
		if length < 4 {
			// Not a frame boundary any more. Nothing downstream can be trusted,
			// so stop sniffing this direction rather than emit garbage.
			s.buf = nil
			return written, nil
		}
		total := length + 1
		if total > maxWireMessage {
			// Skip the message without ever holding it: whatever of it is
			// already buffered is dropped, and the rest is discarded as it
			// arrives. The proxy has forwarded it regardless.
			if int64(len(s.buf)) >= total {
				s.consume(total)
				continue
			}
			s.skip = total - int64(len(s.buf))
			s.buf = nil
			continue
		}
		if int64(len(s.buf)) < total {
			break
		}
		s.onMessage(s.buf[0], s.buf[5:total])
		s.consume(total)
	}
	return written, nil
}

// consume drops the first n bytes, copying the remainder into a fresh slice so
// the buffer does not keep the whole connection's traffic alive behind a
// re-slice.
func (s *pgSniffer) consume(n int64) {
	if int64(len(s.buf)) == n {
		s.buf = nil
		return
	}
	s.buf = append([]byte(nil), s.buf[n:]...)
}

// pgSession decodes one connection's messages into statements. Both directions
// are sniffed on separate goroutines, so every field is guarded.
type pgSession struct {
	label  string // the connection label statements carry
	params bool
	emit   func(connection.Statement)

	mu       sync.Mutex
	prepared map[string]string   // Parse name → SQL
	portals  map[string]pgPortal // Bind portal → what it will execute
	pending  []*pgPending        // issued, awaiting the server's answer
}

type pgPortal struct {
	sql  string
	args []any
}

type pgPending struct {
	started time.Time
	sql     string
	args    []any
}

func newPGSession(label string, params bool, emit func(connection.Statement)) *pgSession {
	return &pgSession{
		label: label, params: params, emit: emit,
		prepared: map[string]string{},
		portals:  map[string]pgPortal{},
	}
}

// frontend decodes a client message. Only the four that carry SQL matter; the
// rest of the protocol is forwarded and ignored.
func (s *pgSession) frontend(typ byte, body []byte) {
	switch typ {
	case 'Q':
		var msg pgproto3.Query
		if msg.Decode(body) == nil {
			s.issue(msg.String, nil)
		}
	case 'P':
		var msg pgproto3.Parse
		if msg.Decode(body) == nil {
			s.mu.Lock()
			s.prepared[msg.Name] = msg.Query
			s.mu.Unlock()
		}
	case 'B':
		var msg pgproto3.Bind
		if msg.Decode(body) == nil {
			s.mu.Lock()
			s.portals[msg.DestinationPortal] = pgPortal{
				sql:  s.prepared[msg.PreparedStatement],
				args: s.bindArgs(msg.Parameters),
			}
			s.mu.Unlock()
		}
	case 'E':
		var msg pgproto3.Execute
		if msg.Decode(body) == nil {
			s.mu.Lock()
			portal := s.portals[msg.Portal]
			s.mu.Unlock()
			s.issue(portal.sql, portal.args)
		}
	}
}

// bindArgs renders bind values only when the capture asked for them. They are
// off by default because bind parameters carry the row data — the likeliest
// place in a capture for a credential or a customer's name.
func (s *pgSession) bindArgs(values [][]byte) []any {
	if !s.params {
		return nil
	}
	args := make([]any, 0, len(values))
	for _, value := range values {
		if value == nil {
			args = append(args, "NULL")
			continue
		}
		args = append(args, string(value))
	}
	return args
}

// backend decodes a server message, completing the oldest statement still
// waiting. A connection answers in order, so the queue is FIFO.
func (s *pgSession) backend(typ byte, body []byte) {
	switch typ {
	case 'C':
		var msg pgproto3.CommandComplete
		if msg.Decode(body) == nil {
			s.complete(rowsFromTag(string(msg.CommandTag)), "")
		}
	case 'I':
		s.complete(0, "")
	case 'E':
		var msg pgproto3.ErrorResponse
		if msg.Decode(body) == nil {
			s.complete(-1, msg.Severity+": "+msg.Message)
		}
	case 'Z':
		// The transaction boundary: anything still waiting got no answer of its
		// own — a statement inside a failed batch, say — and is recorded as
		// issued rather than dropped.
		s.flush()
	}
}

func (s *pgSession) issue(sql string, args []any) {
	if sql == "" {
		return
	}
	s.mu.Lock()
	s.pending = append(s.pending, &pgPending{started: time.Now(), sql: sql, args: args})
	s.mu.Unlock()
}

func (s *pgSession) complete(rows int64, errText string) {
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return
	}
	statement := s.pending[0]
	s.pending = s.pending[1:]
	s.mu.Unlock()

	s.emit(s.statement(statement, rows, errText))
}

// flush records whatever is still waiting, which is how a statement the server
// never answered for still shows up in the capture.
func (s *pgSession) flush() {
	s.mu.Lock()
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()

	for _, statement := range pending {
		s.emit(s.statement(statement, -1, ""))
	}
}

func (s *pgSession) statement(p *pgPending, rows int64, errText string) connection.Statement {
	return connection.Statement{
		Connection: s.label, Driver: driver, SQL: p.sql, Args: p.args,
		StartedAt: p.started, Duration: time.Since(p.started), Rows: rows, Error: errText,
	}
}

// rowsFromTag reads the count off a command tag: "INSERT 0 1", "SELECT 3",
// "UPDATE 2". Tags without one ("BEGIN", "SET") report -1 rather than 0, so a
// reader can tell "no rows" from "not a row-returning statement".
func rowsFromTag(tag string) int64 {
	fields := strings.Fields(tag)
	if len(fields) < 2 {
		return -1
	}
	var count int64
	for _, r := range fields[len(fields)-1] {
		if r < '0' || r > '9' {
			return -1
		}
		count = count*10 + int64(r-'0')
	}
	return count
}
