package db

import (
	"fmt"
	"time"

	rpchttp "github.com/flanksource/clicky/rpc/http"
	"gorm.io/gorm"
)

// timingStartKey names the per-statement start time stashed in Statement.Settings.
const timingStartKey = "commons-db:timing-start"

// timingMetric is the Server-Timing metric name every statement aggregates into.
const timingMetric = "sql"

// serverTimingPlugin attributes database wall time, query count and rows
// returned to the request-scoped rpchttp.Timings accumulator, so a handler can
// report how much of its latency was Postgres. It is a no-op for callers that
// never installed an accumulator (CLI, background workers), which pay one
// context lookup per statement and nothing else.
//
// Every statement contributes its wall time and a queries counter; see withRows
// for which of them can also report rows_returned.
type serverTimingPlugin struct{}

// NewServerTimingPlugin returns a gorm plugin that records an "sql" metric with
// queries and rows_returned counters for every statement whose context carries
// an rpchttp.Timings accumulator.
func NewServerTimingPlugin() gorm.Plugin { return serverTimingPlugin{} }

func (serverTimingPlugin) Name() string { return "commons-db:server-timing" }

func (p serverTimingPlugin) Initialize(db *gorm.DB) error {
	cb := db.Callback()
	hooks := []struct {
		callback interface{ Register(string, func(*gorm.DB)) error }
		hook     func(*gorm.DB)
		name     string
	}{
		{cb.Create().Before("gorm:create"), p.before, "before:create"},
		{cb.Create().After("gorm:create"), p.after(withoutRows), "after:create"},

		{cb.Query().Before("gorm:query"), p.before, "before:select"},
		{cb.Query().After("gorm:query"), p.after(withRows), "after:select"},

		{cb.Delete().Before("gorm:delete"), p.before, "before:delete"},
		{cb.Delete().After("gorm:delete"), p.after(withoutRows), "after:delete"},

		{cb.Update().Before("gorm:update"), p.before, "before:update"},
		{cb.Update().After("gorm:update"), p.after(withoutRows), "after:update"},

		{cb.Row().Before("gorm:row"), p.before, "before:row"},
		{cb.Row().After("gorm:row"), p.after(withoutRows), "after:row"},

		{cb.Raw().Before("gorm:raw"), p.before, "before:raw"},
		{cb.Raw().After("gorm:raw"), p.after(withoutRows), "after:raw"},
	}

	var firstErr error
	for _, h := range hooks {
		if err := h.callback.Register("server-timing:"+h.name, h.hook); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("callback register %s failed: %w", h.name, err)
		}
	}
	return firstErr
}

func (serverTimingPlugin) before(tx *gorm.DB) {
	if !timingsEnabled(tx) {
		return
	}
	tx.Statement.Settings.Store(timingStartKey, time.Now())
}

// withRows marks the callbacks whose row count is already populated when the
// after-hook runs. Only the Query callback qualifies: it scans inside
// "gorm:query" and so has RowsAffected by then. Row/Raw statements (the
// Rows()/Scan() finishers) scan after the whole callback chain has returned, and
// Create/Update/Delete report rows written rather than read — neither
// contributes to rows_returned, so the counter stays a count of rows actually
// read rather than a number that quietly means several different things.
const (
	withRows    = true
	withoutRows = false
)

func (serverTimingPlugin) after(countsRows bool) func(*gorm.DB) {
	return func(tx *gorm.DB) {
		if !timingsEnabled(tx) {
			return
		}
		value, ok := tx.Statement.Settings.LoadAndDelete(timingStartKey)
		if !ok {
			return
		}
		start, ok := value.(time.Time)
		if !ok {
			return
		}
		counters := []rpchttp.TimingCounter{{Name: "queries", Value: 1}}
		// RowsAffected is -1 when the driver reported no count at all.
		if countsRows && tx.Statement.RowsAffected >= 0 {
			counters = append(counters, rpchttp.TimingCounter{Name: "rows_returned", Value: tx.Statement.RowsAffected})
		}
		rpchttp.AddTiming(tx.Statement.Context, rpchttp.TimingMetric{
			Name:     timingMetric,
			Duration: time.Since(start),
			Counters: counters,
		})
	}
}

func timingsEnabled(tx *gorm.DB) bool {
	if tx.Statement == nil || tx.Statement.Context == nil {
		return false
	}
	_, ok := rpchttp.TimingsFromContext(tx.Statement.Context)
	return ok
}
