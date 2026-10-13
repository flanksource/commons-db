package xetrace

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"

	mssql "github.com/microsoft/go-mssqldb"
)

// IsTransientPollError reports whether a ring_buffer read failure is worth
// re-attempting on a fresh connection.
//
// It is an ALLOWLIST: an error this function does not recognise is terminal, so
// a genuine failure (a revoked permission, a dropped session, a malformed
// payload) surfaces immediately with its own first message instead of being
// retried N times and reported late behind a retry-count wrapper.
//
// The canonical member is mssql.StreamError. When a poll's deadline fires
// mid-read the driver sends a TDS attention packet and waits for the server to
// confirm the cancellation; a server still materialising target_data has not
// produced a first byte and does not answer in time, which yields
// "Invalid TDS stream: did not get cancellation confirmation from the server".
// The driver marks that connection bad and database/sql discards it, so the
// next attempt is issued on a healthy connection — which is exactly why
// retrying works.
//
// Pinned connections can surface ErrBadConn or ErrConnDone instead of being
// transparently replaced by database/sql. ErrSessionGone remains terminal.
func IsTransientPollError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrSessionGone) {
		return false
	}
	if errors.Is(err, sql.ErrConnDone) || errors.Is(err, driver.ErrBadConn) {
		return true
	}

	// StreamError is produced as a value, not a pointer — matching on
	// *mssql.StreamError would silently never fire. It also has no Unwrap
	// method, so its InnerError (the deadline, for the cancel-drain case) is
	// visible in the message but NOT reachable via errors.Is; matching the
	// type is the only way to recognise it.
	var stream mssql.StreamError
	if errors.As(err, &stream) {
		return true
	}
	// ServerError means the server severed the connection; a new one may work.
	var server mssql.ServerError
	if errors.As(err, &server) {
		return true
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	// The poll's own budget expiring. Callers MUST rule out parent
	// cancellation before consulting this — a parent deadline surfaces as the
	// same error and means shutdown, not failure.
	return errors.Is(err, context.DeadlineExceeded)
}
