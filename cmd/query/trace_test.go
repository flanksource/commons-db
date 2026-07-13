package main

import (
	"testing"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/stretchr/testify/require"
)

func TestApplySessionSpecOverrides(t *testing.T) {
	t.Run("synthesizes top for a plain profile", func(t *testing.T) {
		p, err := applySessionSpecOverrides(execProfile("plain"), "2s", "")
		require.NoError(t, err)
		require.Equal(t, query.KindTop, p.Kind())
		require.Equal(t, 2*time.Second, p.Top.Interval.Duration)
	})

	t.Run("rejects a plain profile without an interval", func(t *testing.T) {
		_, err := applySessionSpecOverrides(execProfile("plain"), "", "")
		require.ErrorContains(t, err, "neither trace nor top")
	})

	t.Run("overrides a declared top interval and duration", func(t *testing.T) {
		in := execProfile("declared")
		in.Top = &query.TopSpec{}
		p, err := applySessionSpecOverrides(in, "10s", "1m")
		require.NoError(t, err)
		require.Equal(t, 10*time.Second, p.Top.Interval.Duration)
		require.Equal(t, time.Minute, p.Top.MaxDuration.Duration)
	})

	t.Run("rejects an interval on a trace profile", func(t *testing.T) {
		_, err := applySessionSpecOverrides(traceTestProfile("t", "any"), "2s", "")
		require.ErrorContains(t, err, "interval does not apply")
	})

	t.Run("overrides trace duration", func(t *testing.T) {
		p, err := applySessionSpecOverrides(traceTestProfile("t", "any"), "", "30s")
		require.NoError(t, err)
		require.Equal(t, 30*time.Second, p.Trace.MaxDuration.Duration)
	})

	t.Run("rejects malformed durations", func(t *testing.T) {
		_, err := applySessionSpecOverrides(traceTestProfile("t", "any"), "", "soon")
		require.ErrorContains(t, err, "invalid duration")
		_, err = applySessionSpecOverrides(execProfile("p"), "fast", "")
		require.ErrorContains(t, err, "invalid interval")
	})
}

func TestParseSessionParams(t *testing.T) {
	params, err := parseSessionParams([]string{"region=EU", "pod=api-0"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"region": "EU", "pod": "api-0"}, params)

	_, err = parseSessionParams([]string{"no-equals"})
	require.ErrorContains(t, err, "expected key=value")
}

func TestFormatTraceEventLine(t *testing.T) {
	ts := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	e := query.Event{Sequence: 3, Time: ts, Row: query.Row{"msg": "hello world", "level": "info"}}
	line := formatTraceEventLine(e)
	require.Equal(t, `2026-07-13T10:00:00Z level=info msg="hello world"`, line)

	errLine := formatTraceEventLine(query.Event{Time: ts, Error: "backend gone"})
	require.Equal(t, `2026-07-13T10:00:00Z error="backend gone"`, errLine)
}
