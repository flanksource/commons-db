package xetrace

import (
	"strings"
	"testing"

	"github.com/flanksource/commons/properties"
)

// The ring buffer cap is the difference between a busy instance's events being
// read and being FIFO-evicted before the next poll, and SQL Server does not
// report that eviction in droppedCount — it surfaces only as the drain's own
// "lost N event(s)" delta. Pin the default so a regression is loud.
func TestDefaultRingBufferEvents(t *testing.T) {
	if got := defaultRingBufferEvents(); got != 8092 {
		t.Fatalf("defaultRingBufferEvents() = %d, want 8092", got)
	}
}

func TestRingBufferEventsRespectsProperty(t *testing.T) {
	const key = "sqltrace.ringBuffer.maxEvents"
	prev := properties.String("", key)
	properties.Set(key, "250")
	t.Cleanup(func() { properties.Set(key, prev) })

	if got := defaultRingBufferEvents(); got != 250 {
		t.Fatalf("defaultRingBufferEvents() = %d, want the property override 250", got)
	}
}

// A non-positive override is a misconfiguration, not an instruction to build a
// zero-capacity buffer that captures nothing.
func TestRingBufferEventsIgnoresNonPositiveOverride(t *testing.T) {
	const key = "sqltrace.ringBuffer.maxEvents"
	prev := properties.String("", key)
	t.Cleanup(func() { properties.Set(key, prev) })

	for _, v := range []string{"0", "-1"} {
		properties.Set(key, v)
		if got := defaultRingBufferEvents(); got != 8092 {
			t.Fatalf("override %q: defaultRingBufferEvents() = %d, want the 8092 fallback", v, got)
		}
	}
}

// The event cap only binds if the buffer has the memory to reach it, so the two
// defaults have to be read together: whichever is hit first stops the buffer.
func TestDefaultRingBufferSizingIsCarriedIntoDDL(t *testing.T) {
	opts := CreateOptions{
		Name:         "commons_db_trace_test",
		DatabaseName: "warehouse",
		Events:       DefaultEvents,
		MaxMemoryKB:  defaultRingBufferMemoryKB(),
		MaxEvents:    defaultRingBufferEvents(),
	}
	got, err := BuildCreateSQL(opts)
	if err != nil {
		t.Fatalf("BuildCreateSQL: %v", err)
	}
	want := "ADD TARGET package0.ring_buffer (SET max_memory = 4096, max_events_limit = 8092)"
	if !strings.Contains(got, want) {
		t.Fatalf("DDL missing %q\nfull:\n%s", want, got)
	}
}
