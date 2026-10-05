// Registers a gorm plugin's before and after hooks around every kind of
// statement gorm runs: creates, queries, deletes, updates, rows and raw SQL.

package db

import (
	"fmt"

	"gorm.io/gorm"
)

// withRows marks the callbacks whose row count is already populated when the
// after-hook runs. Only the Query callback qualifies: it scans inside
// "gorm:query" and so has RowsAffected by then. Row/Raw statements (the
// Rows()/Scan() finishers) scan after the whole callback chain has returned, and
// Create/Update/Delete report rows written rather than read.
const (
	withRows    = true
	withoutRows = false
)

// registerStatementHooks registers before ahead of, and after behind, every
// statement callback, naming them <prefix>:before:<op> and <prefix>:after:<op>.
// after is told whether its callback's RowsAffected counts rows read.
func registerStatementHooks(db *gorm.DB, prefix string, before func(*gorm.DB), after func(countsRows bool) func(*gorm.DB)) error {
	cb := db.Callback()
	hooks := []struct {
		callback interface {
			Register(string, func(*gorm.DB)) error
		}
		hook func(*gorm.DB)
		name string
	}{
		{cb.Create().Before("gorm:create"), before, "before:create"},
		{cb.Create().After("gorm:create"), after(withoutRows), "after:create"},

		{cb.Query().Before("gorm:query"), before, "before:select"},
		{cb.Query().After("gorm:query"), after(withRows), "after:select"},

		{cb.Delete().Before("gorm:delete"), before, "before:delete"},
		{cb.Delete().After("gorm:delete"), after(withoutRows), "after:delete"},

		{cb.Update().Before("gorm:update"), before, "before:update"},
		{cb.Update().After("gorm:update"), after(withoutRows), "after:update"},

		{cb.Row().Before("gorm:row"), before, "before:row"},
		{cb.Row().After("gorm:row"), after(withoutRows), "after:row"},

		{cb.Raw().Before("gorm:raw"), before, "before:raw"},
		{cb.Raw().After("gorm:raw"), after(withoutRows), "after:raw"},
	}

	var firstErr error
	for _, h := range hooks {
		if err := h.callback.Register(prefix+":"+h.name, h.hook); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("callback register %s failed: %w", h.name, err)
		}
	}
	return firstErr
}
