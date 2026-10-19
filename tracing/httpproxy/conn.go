package httpproxy

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// halfCloser is goproxy's halfClosable. goproxy takes a materially better
// teardown path for a dialed connection that is one, so the counting wrapper
// keeps the capability rather than hiding it.
type halfCloser interface {
	net.Conn
	CloseWrite() error
	CloseRead() error
}

// countingConn wraps the proxy-to-server side of a CONNECT tunnel, counting
// the bytes crossing it: reads carry the server's response (bytes in), writes
// the client's request (bytes out).
type countingConn struct {
	net.Conn
	recorder *recorder
	host     string
	serverIP string
	started  time.Time
	in       atomic.Int64
	out      atomic.Int64
	once     sync.Once
	// self is what the recorder tracks: the half-closable wrapper when there
	// is one, which untracking c instead would leave behind.
	self net.Conn
}

// newCountingConn wraps inner keeping the capabilities it has: claiming
// CloseWrite/CloseRead unconditionally would push goproxy down its fast path
// with a connection that cannot honour it.
func newCountingConn(inner net.Conn, recorder *recorder, host string, started time.Time) net.Conn {
	counting := &countingConn{Conn: inner, recorder: recorder, host: host, serverIP: remoteIP(inner), started: started}
	counting.self = counting
	if _, ok := inner.(halfCloser); ok {
		wrapped := &countingHalfCloser{countingConn: counting}
		counting.self = wrapped
		return wrapped
	}
	return counting
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.in.Add(int64(n))
	return n, err
}

func (c *countingConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.out.Add(int64(n))
	return n, err
}

func (c *countingConn) Close() error {
	c.finish()
	return c.Conn.Close()
}

// finish records the tunnel once: goproxy closes both ends of a tunnel and
// can close the same connection twice.
func (c *countingConn) finish() {
	c.once.Do(func() {
		c.recorder.untrack(c.self)
		c.recorder.add(connectExchange(c.host, c.serverIP, c.started, time.Since(c.started), c.in.Load(), c.out.Load(), nil))
	})
}

// countingHalfCloser forwards the half-close methods to the inner connection.
type countingHalfCloser struct {
	*countingConn
}

func (c *countingHalfCloser) CloseWrite() error { return c.inner().CloseWrite() }
func (c *countingHalfCloser) CloseRead() error  { return c.inner().CloseRead() }

func (c *countingHalfCloser) inner() halfCloser { return c.Conn.(halfCloser) }

// remoteIP is the IP conn reached: the server, or the upstream proxy a
// tunnel is chained through.
func remoteIP(conn net.Conn) string {
	if addr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		return addr.IP.String()
	}
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return ""
	}
	return host
}
