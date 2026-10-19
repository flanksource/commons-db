package pgproxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons-db/connection"
)

// Postgres startup codes that arrive in place of a StartupMessage. The proxy
// declines both: it forwards a plain stream to the upstream, so a client that
// negotiated TLS with us would be talking into a tunnel we cannot re-establish.
//
// CancelRequest (80877102) is deliberately absent: it arrives on its own
// connection carrying no SQL and is forwarded like any other startup packet.
const (
	sslRequestCode    = 80877103
	gssEncRequestCode = 80877104
)

const dialTimeout = 10 * time.Second

// proxy is a postgres wire proxy a process is pointed at. It forwards bytes
// verbatim in both directions and decodes a copy, so extended-query, COPY and
// pipelined traffic pass through unchanged: a re-serialising proxy corrupts
// those in ways that present as flaky tests.
type proxy struct {
	params   Params
	upstream string   // host:port the traffic is forwarded to
	dsn      *url.URL // the resolved upstream DSN, rewritten for the child
	listener net.Listener
	emit     func(connection.Statement)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	conns  []net.Conn
	closed bool
}

// startProxy listens on a loopback port and serves until Close. The upstream
// must accept unencrypted connections: the proxy answers a client's SSLRequest
// with a refusal and dials the upstream in the clear.
func startProxy(params Params, dsn *url.URL, emit func(connection.Statement)) (*proxy, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("pg_proxy listen: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &proxy{params: params, upstream: upstreamAddr(dsn), dsn: dsn, listener: listener, emit: emit, ctx: ctx, cancel: cancel}
	p.wg.Add(1)
	go p.serve()
	return p, nil
}

// Addr is the proxy's host:port.
func (p *proxy) Addr() string { return p.listener.Addr().String() }

// childEnv is the environment a process needs so its queries are observed: the
// same DSN with the host and port swapped for the proxy's, plus the libpq
// variables a client that builds its own connection string reads.
//
// sslmode is forced off because the proxy refuses TLS; leaving a `require` in
// the DSN would make the child fail to connect rather than fail to be recorded.
func (p *proxy) childEnv() (map[string]string, error) {
	rewritten := *p.dsn
	rewritten.Host = p.Addr()

	query := rewritten.Query()
	query.Set("sslmode", "disable")
	rewritten.RawQuery = query.Encode()

	host, port, err := net.SplitHostPort(p.Addr())
	if err != nil {
		return nil, fmt.Errorf("pg_proxy listener address %q: %w", p.Addr(), err)
	}
	env := map[string]string{
		"GAVEL_DB_DSN": rewritten.String(),
		// Rewritten too: it is where the DSN most often came from, and a child
		// reading the original would bypass the proxy and record nothing.
		"DATABASE_URL": rewritten.String(),
		"PGHOST":       host,
		"PGPORT":       port,
		"PGSSLMODE":    "disable",
	}
	if p.params.Env != "" {
		env[p.params.Env] = rewritten.String()
	}
	if database := strings.TrimPrefix(rewritten.Path, "/"); database != "" {
		env["PGDATABASE"] = database
	}
	// libpq otherwise logs in as the OS user, a role the upstream rarely has.
	if user := rewritten.User.Username(); user != "" {
		env["PGUSER"] = user
	}
	return env, nil
}

func (p *proxy) serve() {
	defer p.wg.Done()
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return // Close closed the listener
		}
		if !p.track(client) {
			continue
		}
		p.wg.Add(1)
		go p.handle(client)
	}
}

// handle proxies one client connection, sniffing a copy of each direction.
func (p *proxy) handle(client net.Conn) {
	defer p.wg.Done()
	defer client.Close()

	dialer := net.Dialer{Timeout: dialTimeout}
	server, err := dialer.DialContext(p.ctx, "tcp", p.upstream)
	if err != nil {
		p.emit(connection.Statement{
			Connection: p.params.Connection, Driver: driver, SQL: "CONNECT " + p.upstream,
			StartedAt: time.Now(), Rows: -1, Error: fmt.Sprintf("dial %s: %v", p.upstream, err),
		})
		return
	}
	defer server.Close()
	if !p.track(server) {
		return
	}

	if err := negotiateStartup(client, server); err != nil {
		return
	}

	session := newPGSession(p.params.Connection, p.params.Params, p.emit)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The sniffer comes before the server: io.MultiWriter runs its writers in
		// order, so forwarding first lets the server answer a statement this
		// session has not recorded yet. complete() would then find nothing
		// pending and drop the answer, and the statement would surface from
		// flush() with no row count and no error — the server's reply lost even
		// though the client saw it. Recording first makes the pending entry
		// exist before the query can be asked.
		_, _ = io.Copy(io.MultiWriter(&pgSniffer{onMessage: session.frontend}, server), client)
		// Half-close so the server sees the client's EOF and answers it,
		// instead of both sides waiting on the other.
		if half, ok := server.(*net.TCPConn); ok {
			_ = half.CloseWrite()
		}
	}()

	// Sniff before forwarding so the client cannot observe ReadyForQuery and
	// issue its next statement before the recorder has processed that boundary.
	_, _ = io.Copy(io.MultiWriter(&pgSniffer{onMessage: session.backend}, client), server)
	<-done
	session.flush()
}

// negotiateStartup forwards the client's StartupMessage, refusing any TLS or
// GSS negotiation that precedes it. A refusal is what the protocol expects
// ('N'), so a client configured for `sslmode=prefer` falls back cleanly.
func negotiateStartup(client, server net.Conn) error {
	for {
		header := make([]byte, 8)
		if _, err := io.ReadFull(client, header); err != nil {
			return err
		}
		length := int(binary.BigEndian.Uint32(header[:4]))
		if length < 8 || length > maxWireMessage {
			return fmt.Errorf("pg_proxy startup packet length %d out of range", length)
		}

		switch binary.BigEndian.Uint32(header[4:8]) {
		case sslRequestCode, gssEncRequestCode:
			if _, err := client.Write([]byte{'N'}); err != nil {
				return err
			}
			continue
		}

		packet := make([]byte, length)
		copy(packet, header)
		if _, err := io.ReadFull(client, packet[8:]); err != nil {
			return err
		}
		_, err := server.Write(packet)
		return err
	}
}

// track remembers conn so Close can drop it, closing it at once and reporting
// false when the proxy is already closed.
func (p *proxy) track(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = conn.Close()
		return false
	}
	p.conns = append(p.conns, conn)
	return true
}

// Close stops accepting, drops every connection and waits for their sessions
// to record what was still pending. There is no drain window: a postgres
// connection is long-lived by design, so waiting for one to end on its own
// would wait for the client to exit.
func (p *proxy) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	conns := append([]net.Conn(nil), p.conns...)
	p.mu.Unlock()

	p.cancel()
	err := p.listener.Close()
	for _, conn := range conns {
		_ = conn.Close()
	}
	p.wg.Wait()
	return err
}
