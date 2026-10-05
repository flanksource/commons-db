// A gorm plugin that publishes every statement a handle runs to the SQL
// statement tap while anyone observes every connection's statements.

package db

import (
	"time"

	"gorm.io/gorm"

	"github.com/flanksource/commons-db/connection"
)

// statementStartKey names the per-statement start time stashed in Statement.Settings.
const statementStartKey = "commons-db:statement-start"

type sqlStatementPlugin struct{}

// NewSQLStatementPlugin returns a gorm plugin that publishes each statement the
// handle runs (connection.PublishSQL) as the context's own database's, which
// has no connection name. A handle nobody observes pays one lookup per statement.
func NewSQLStatementPlugin() gorm.Plugin { return sqlStatementPlugin{} }

func (sqlStatementPlugin) Name() string { return "commons-db:sql-statements" }

func (p sqlStatementPlugin) Initialize(db *gorm.DB) error {
	return registerStatementHooks(db, "sql-statements", p.before, p.after)
}

func (sqlStatementPlugin) before(tx *gorm.DB) {
	if tx.Statement == nil || !connection.ObservingSQL("") {
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
			Driver: tx.Dialector.Name(), SQL: tx.Statement.SQL.String(), Args: tx.Statement.Vars,
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
