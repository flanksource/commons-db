// A gorm plugin that publishes every statement a handle runs to the SQL
// statement tap while anyone observes the context's own database.

package db

import (
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/flanksource/commons-db/connection"
)

// statementStartKey names the per-statement start time stashed in Statement.Settings.
const statementStartKey = "commons-db:statement-start"

type sqlStatementPlugin struct{}

// NewSQLStatementPlugin returns a gorm plugin that publishes each statement the
// handle runs (connection.PublishSQL) as the context's own database's
// (connection.OwnDatabase). A handle nobody observes pays one lookup per statement.
func NewSQLStatementPlugin() gorm.Plugin { return sqlStatementPlugin{} }

func (sqlStatementPlugin) Name() string { return "commons-db:sql-statements" }

func (p sqlStatementPlugin) Initialize(db *gorm.DB) error {
	return registerStatementHooks(db, "sql-statements", p.before, p.after)
}

func (sqlStatementPlugin) before(tx *gorm.DB) {
	if tx.Statement == nil || !connection.ObservingSQL(connection.OwnDatabase) {
		return
	}
	tx.Statement.Settings.Store(statementStartKey, time.Now())
}

func (sqlStatementPlugin) after(countsRows bool) func(*gorm.DB) {
	return func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		value, ok := tx.Statement.Settings.LoadAndDelete(statementStartKey)
		if !ok {
			return
		}
		started, ok := value.(time.Time)
		if !ok {
			return
		}
		statement := connection.Statement{
			Connection: connection.OwnDatabase, Driver: tx.Dialector.Name(), SQL: tx.Statement.SQL.String(), Args: tx.Statement.Vars,
			StartedAt: started, Duration: time.Since(started), Rows: -1,
		}
		if countsRows {
			statement.Rows = tx.Statement.RowsAffected
		}
		if tx.Error != nil {
			statement.Error = tx.Error.Error()
		}
		connection.PublishSQL(statement)
	}
}

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
