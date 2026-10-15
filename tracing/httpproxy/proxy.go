package httpproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path"
	"sync"
	"time"

	"github.com/elazarl/goproxy"

	tracehttp "github.com/flanksource/commons-db/tracing/http"
)

// drainTimeout bounds a recorder's close: it waits this long for in-flight
// exchanges and tunnels to finish on their own, then force-closes them, so
// one hung client never hangs the capture's end.
const drainTimeout = 2 * time.Second

// recorder is the proxy a capture's processes are pointed at, emitting each
// exchange it sees. Under connect mode a TLS tunnel is one exchange counting
// its bytes; under mitm each request decrypted inside it is one.
type recorder struct {
	params   Params
	rules    policy
	listener net.Listener
	server   *http.Server
	// upstream chains to the host's own proxy, from its HTTPS_PROXY. goproxy
	// would chain itself through ConnectDial, but ConnectDial takes precedence
	// over the per-tunnel dialer that counts bytes, so it is re-done here.
	upstream func(network, addr string) (net.Conn, error)
	// ca is set only in mitm mode.
	ca   *authority
	emit func(tracehttp.Exchange)

	mu      sync.Mutex
	conns   map[net.Conn]struct{}
	streams map[*streamCapture]tracehttp.Exchange
	closing bool
	// ended refuses exchanges finishing once the recorder has closed, after
	// the capture they belong to.
	ended bool
}

// listen starts a recorder on a loopback port it keeps, so nothing can take
// the port between choosing and binding it.
func listen(params Params, rules policy, ca *authority, emit func(tracehttp.Exchange), warn func(string)) (*recorder, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("http proxy listen: %w", err)
	}
	r := &recorder{
		params: params, rules: rules, listener: listener, ca: ca, emit: emit,
		conns: map[net.Conn]struct{}{}, streams: map[*streamCapture]tracehttp.Exchange{},
	}
	proxy := goproxy.NewProxyHttpServer()
	proxy.Logger = silentLogger{}
	r.upstream, proxy.ConnectDial = proxy.ConnectDial, nil
	proxy.OnRequest().HandleConnectFunc(r.handleConnect)
	proxy.OnRequest().DoFunc(r.onRequest)
	proxy.OnResponse().DoFunc(r.onResponse)
	r.server = &http.Server{Handler: proxy, ReadHeaderTimeout: 30 * time.Second}
	go func() {
		if err := r.server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			warn(fmt.Sprintf("http proxy %s stopped serving: %v", listener.Addr(), err))
		}
	}()
	return r, nil
}

func (r *recorder) addr() string { return r.listener.Addr().String() }

// records reports whether host, with or without its port, is in scope.
func (r *recorder) records(host string) bool {
	return matchesAny(r.params.Hosts, hostOnly(host))
}

// recordsExchange reports whether a request seen in the clear is in scope:
// its host under hosts and its path under paths.
func (r *recorder) recordsExchange(target *url.URL) bool {
	return r.records(target.Host) && matchesAny(r.params.Paths, target.Path)
}

// matchesAny reports whether value matches one of patterns; no patterns
// match every value.
func matchesAny(patterns []string, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if ok, _ := path.Match(pattern, value); ok {
			return true
		}
	}
	return false
}

// handleConnect accepts every tunnel. A host in scope is decrypted under
// mitm, its exchanges recorded by the same handlers as plain HTTP; otherwise
// its tunnel is dialed through a connection counting its own bytes, which
// leaves goproxy's half-close handling exactly as it is. A host out of scope
// stays encrypted and unrecorded, so hosts bounds what mitm decrypts.
func (r *recorder) handleConnect(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
	if !r.records(host) {
		ctx.Dialer = func(_ context.Context, network, addr string) (net.Conn, error) { return r.dial(network, addr) }
		return goproxy.OkConnect, host
	}
	if r.ca != nil {
		return &goproxy.ConnectAction{Action: goproxy.ConnectMitm, TLSConfig: goproxy.TLSConfigFromCA(&r.ca.cert)}, host
	}
	started := time.Now()
	ctx.Dialer = func(_ context.Context, network, addr string) (net.Conn, error) {
		conn, err := r.dial(network, addr)
		if err != nil {
			r.add(connectExchange(host, "", started, time.Since(started), 0, 0, err))
			return nil, err
		}
		return r.track(newCountingConn(conn, r, host, started)), nil
	}
	return goproxy.OkConnect, host
}

func (r *recorder) dial(network, addr string) (net.Conn, error) {
	if r.upstream != nil {
		return r.upstream(network, addr)
	}
	return net.Dial(network, addr)
}

// onRequest stamps the request's start and captures its body before the
// transport consumes it; goproxy hands the same ProxyCtx to onResponse.
func (r *recorder) onRequest(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
	state := exchangeState{started: time.Now()}
	if r.rules.bodies > 0 && req.Body != nil && r.recordsExchange(req.URL) {
		state.post, state.comment = r.rules.capturePostData(req)
	}
	ctx.UserData = state
	return req, nil
}

func (r *recorder) onResponse(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
	if ctx.Req == nil || !r.recordsExchange(ctx.Req.URL) {
		return resp
	}
	state, ok := ctx.UserData.(exchangeState)
	if !ok {
		state = exchangeState{started: time.Now(), comment: "request start not observed"}
	}
	exchange, stream := exchangeOf(r.rules, ctx.Req, resp, state, ctx.Error)
	if stream == nil {
		r.add(exchange)
		return resp
	}
	stream.ended = r.streamEnded
	r.mu.Lock()
	r.streams[stream] = exchange
	r.mu.Unlock()
	return resp
}

// streamEnded emits an event stream's exchange once the stream has ended,
// unless the recorder already emitted it as it closed.
func (r *recorder) streamEnded(stream *streamCapture) {
	r.mu.Lock()
	defer r.mu.Unlock()
	exchange, ok := r.streams[stream]
	if !ok || r.ended {
		return
	}
	delete(r.streams, stream)
	stream.apply(&exchange)
	r.emit(exchange)
}

func (r *recorder) add(exchange tracehttp.Exchange) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.ended {
		r.emit(exchange)
	}
}

func (r *recorder) track(conn net.Conn) net.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conns[conn] = struct{}{}
	return conn
}

func (r *recorder) untrack(conn net.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.conns, conn)
}

// close stops accepting, waits out the drain window for in-flight exchanges,
// then force-closes what is left: every tunnel, which Shutdown never tracks
// since it is hijacked, and every connection still serving. An event stream
// still open is emitted with what has passed. Closing twice is a no-op.
func (r *recorder) close() error {
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return nil
	}
	r.closing = true
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()
	err := r.server.Shutdown(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		if err = r.server.Close(); errors.Is(err, net.ErrClosed) {
			err = nil
		}
	}
	r.mu.Lock()
	conns := make([]net.Conn, 0, len(r.conns))
	for conn := range r.conns {
		conns = append(conns, conn)
	}
	r.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
	r.flushStreams()
	return err
}

// flushStreams emits each event stream still open, and refuses whatever
// finishes from now on.
func (r *recorder) flushStreams() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for stream, exchange := range r.streams {
		stream.apply(&exchange)
		r.emit(exchange)
	}
	r.streams, r.ended = nil, true
}

// hostOnly strips a :port suffix, leaving an IPv6 literal intact.
func hostOnly(host string) string {
	if stripped, _, err := net.SplitHostPort(host); err == nil {
		return stripped
	}
	return host
}

// silentLogger drops goproxy's chatter, which would otherwise interleave with
// the host's output; a transport failure is still recorded, as its exchange's
// error.
type silentLogger struct{}

func (silentLogger) Printf(string, ...any) {}
