package xetrace

import (
	"time"

	"github.com/flanksource/commons/properties"
)

// Operational tunables for XE capture, read from commons/properties so they
// tune via -P / env / a props file without touching config.Config:
//
//	sqltrace.poll.timeout            per-poll deadline for one ring_buffer read
//	sqltrace.poll.retries            transient poll failures tolerated in a row
//	sqltrace.poll.retryDelay         pause before re-attempting a failed poll
//	sqltrace.ringBuffer.maxMemoryKb  ring buffer size when the caller sets none
//	sqltrace.ringBuffer.maxEvents    ring buffer event cap when the caller sets none
//
// The poll interval is deliberately absent: `--poll` and the `poll` request
// field already own it, and a second source of truth would be worse than none.
const (
	// defaultPollTimeout bounds one ring_buffer read. It is generous because
	// cancelling is expensive, not cheap: when the deadline fires the driver
	// sends a TDS attention packet and waits up to two 5s windows for the
	// server's confirmation, so a poll that gives up early still costs ~10s and
	// holds a pooled connection for all of it. Better to let a slow read finish.
	defaultPollTimeout = 30 * time.Second

	// defaultPollRetries is how many ADDITIONAL attempts a transient failure
	// gets after the first — 2 here means at most 3 polls before Drain gives up.
	// Kept low on purpose: a failed 30s poll can occupy one of only 8 read-pool
	// connections for ~40s, so a deep retry budget starves the rest of the
	// process.
	defaultPollRetries = 2

	defaultPollRetryDelay = 2 * time.Second

	// Managed SQL Server instances cap ring-buffer target memory at 4 MB. Keep
	// the default portable; callers on servers that support larger buffers can
	// still set maxMemoryKb explicitly or override the property.
	defaultRingBufferMemoryKBValue = 4096

	// defaultRingBufferEventsValue caps how many events the ring buffer holds
	// before FIFO-evicting the oldest. SQL Server does not report that eviction
	// in droppedCount, so it surfaces only as the drain's own delta — which is
	// why that delta has to be trustworthy (it was not; see above).
	//
	// This is a cap, not an allocation: the buffer still stops at
	// maxMemoryKb, whichever binds first.
	defaultRingBufferEventsValue = 8092
)

// PollTimeout is the per-poll ring_buffer read deadline. Exported because the
// registry sizes its stop-wait budget from it.
func PollTimeout() time.Duration {
	return properties.Duration(defaultPollTimeout, "sqltrace.poll.timeout")
}

// DropTimeout is the fixed budget Session.Drop gives itself. Exported for the
// same reason as PollTimeout: a caller waiting on a drain has to cover both.
func DropTimeout() time.Duration { return dropTimeout }

func pollRetries() int {
	n := properties.Int(defaultPollRetries, "sqltrace.poll.retries")
	if n < 0 {
		return 0
	}
	return n
}

func pollRetryDelay() time.Duration {
	return properties.Duration(defaultPollRetryDelay, "sqltrace.poll.retryDelay")
}

// DefaultRingBufferMemoryKB is the ring buffer's size cap in KB. Exported for
// the same reason as PollTimeout: a caller that has to stay bigger than one
// poll's worth of events needs to size against it rather than hardcode a number
// that silently becomes too small when this default moves.
func DefaultRingBufferMemoryKB() int { return defaultRingBufferMemoryKB() }

func defaultRingBufferMemoryKB() int {
	return positiveOr(properties.Int(defaultRingBufferMemoryKBValue, "sqltrace.ringBuffer.maxMemoryKb"), defaultRingBufferMemoryKBValue)
}

func defaultRingBufferEvents() int {
	return positiveOr(properties.Int(defaultRingBufferEventsValue, "sqltrace.ringBuffer.maxEvents"), defaultRingBufferEventsValue)
}

func positiveOr(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}
