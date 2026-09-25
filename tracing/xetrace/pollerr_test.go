package xetrace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	mssql "github.com/microsoft/go-mssqldb"
)

// timeoutNetError is a net.Error that reports Timeout() — the shape a stalled
// socket read surfaces as.
type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "i/o timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

var _ net.Error = timeoutNetError{}

func TestIsTransientPollError(t *testing.T) {
	// The exact error the reported failure produces: the driver's attention
	// packet went unanswered because the poll context expired mid-read.
	cancelDrain := mssql.StreamError{
		InnerError: fmt.Errorf("did not get cancellation confirmation from the server (current response: %w", context.DeadlineExceeded),
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"stream error value", cancelDrain, true},
		{"stream error wrapped by Poll", fmt.Errorf("read ring_buffer target: %w", cancelDrain), true},
		{"server error", mssql.ServerError{}, true},
		{"poll deadline", context.DeadlineExceeded, true},
		{"poll deadline wrapped", fmt.Errorf("read ring_buffer target: %w", context.DeadlineExceeded), true},
		{"eof", io.EOF, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"net timeout", timeoutNetError{}, true},

		// Terminal: retrying cannot help, and burying the first message behind
		// a retry count would make the real cause harder to see.
		{"session gone", fmt.Errorf("%w: %q", ErrSessionGone, "s"), false},
		{"no rows", sql.ErrNoRows, false},
		{"context canceled", context.Canceled, false},
		{"unrecognised", errors.New("boom"), false},
		{"permission denied", &PermissionError{Report: PermissionReport{Login: "analytics"}}, false},
		{"parse failure", fmt.Errorf("decode ring_buffer xml: %w", errors.New("EOF token")), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransientPollError(tc.err); got != tc.want {
				t.Errorf("IsTransientPollError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsTransientPollError_StreamErrorIsAValue pins the trap that would make
// the whole retry path silently dead: go-mssqldb builds StreamError as a value,
// so an errors.As against *mssql.StreamError never matches.
func TestIsTransientPollError_StreamErrorIsAValue(t *testing.T) {
	err := error(mssql.StreamError{InnerError: errors.New("x")})

	var byPointer *mssql.StreamError
	if errors.As(err, &byPointer) {
		t.Fatal("StreamError now matches by pointer; simplify IsTransientPollError")
	}
	var byValue mssql.StreamError
	if !errors.As(err, &byValue) {
		t.Fatal("StreamError must match by value")
	}
}
