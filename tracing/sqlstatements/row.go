// A captured statement as its stream stores it, and as its result profile
// renders it: the columns, and the cells styled as an xevent's are.

package sqlstatements

import (
	"time"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/query/sqltext"
	"github.com/flanksource/commons-db/tracing"
)

// RowKind is the record kind a capture's stream holds, and the kind a host
// registers Kind as: one StatementRow per statement.
const RowKind = "sql_statements"

// StatementRow is one statement as a capture stores it. Op and Tables are
// derived from its text as xetrace classifies a captured event's, and
// RepeatKey keys the statements that differ only by their values.
type StatementRow struct {
	tracing.Truncation
	StartedAt  time.Time `json:"startedAt" pretty:"label=Started" sort:"startedAt"`
	Connection string    `json:"connection" filter:"terms"`
	Driver     string    `json:"driver" filter:"terms"`
	Op         string    `json:"op" pretty:"label=Op" filter:"terms"`
	Tables     []string  `json:"tables" filter:"terms"`
	SQL        string    `json:"sql" pretty:"label=SQL" filter:"text"`
	RepeatKey  string    `json:"repeatKey" pretty:"hide" filter:"exact"`
	Args       []any     `json:"args,omitempty" pretty:"hide"`
	DurationMs float64   `json:"durationMs" pretty:"label=Duration,type=duration,unit=ms" sort:"durationMs"`
	// Rows is how many rows the statement returned, or -1 when its driver
	// could not tell.
	Rows  int64  `json:"rows" sort:"rows"`
	Error string `json:"error,omitempty" filter:"text"`
}

// FromStatement is the row a capture stores for statement. A statement naming
// no table has an empty list of them, never none.
func FromStatement(statement connection.Statement) StatementRow {
	tables := sqltext.ExtractTables(statement.SQL)
	if tables == nil {
		tables = []string{}
	}
	return StatementRow{
		StartedAt: statement.StartedAt.UTC(), Connection: statement.Connection, Driver: statement.Driver,
		Op: string(sqltext.ClassifyStatement(statement.SQL)), Tables: tables,
		SQL: statement.SQL, RepeatKey: RepeatKey(statement.SQL), Args: statement.Args,
		DurationMs: float64(statement.Duration.Microseconds()) / 1000,
		Rows:       statement.Rows, Error: statement.Error,
	}
}

func (StatementRow) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("startedAt").Label("Time").Kind("timestamp").Build(),
		api.Column("sql").Label("Statement").MinWidthPixels(360).MaxWidthPixels(720).Build(),
		api.Column("op").Label("Op").Build(),
		api.Column("tables").Label("Tables").Kind("tags").Build(),
		api.Column("durationMs").Label("Duration").Style("text-right").Type("duration").Unit("ms").FilterKey("filter.durationMs").Build(),
		api.Column("rows").Label("Rows").Style("text-right").Type("number").FilterKey("filter.rows").Build(),
		api.Column("connection").Label("Connection").DefaultHidden().Build(),
		api.Column("driver").Label("Driver").DefaultHidden().Build(),
		api.Column("error").Hidden().Build(),
		api.Column("args").Kind("json").Hidden().Build(),
		api.Column("repeatKey").Hidden().Build(),
		api.Column("truncated").Hidden().Build(),
	}
}

func (r StatementRow) Row() map[string]any {
	duration := milliseconds(r.DurationMs)
	rows := api.Text{}
	if r.Rows >= 0 {
		rows = api.HumanNumber(r.Rows, "text-muted")
	}
	return map[string]any{
		"startedAt":  r.StartedAt,
		"sql":        api.TableCell{Value: r.statementCell(), FilterValue: r.SQL},
		"op":         r.Op,
		"tables":     r.Tables,
		"durationMs": api.TableCell{Value: api.Human(duration, sqltext.DurationStyle(duration)), FilterValue: r.DurationMs},
		"rows":       api.TableCell{Value: rows, FilterValue: r.Rows},
		"connection": r.Connection, "driver": r.Driver, "error": r.Error,
		"args": r.Args, "repeatKey": r.RepeatKey, "truncated": r.Truncated,
	}
}

// statementCell is the statement's SQL, and for a statement that failed, its
// error in red beneath it.
func (r StatementRow) statementCell() api.Textable {
	code := api.CodeBlock("text/x-sql", r.SQL)
	if r.Error == "" {
		return code
	}
	return api.Text{}.Add(code).NewLine().AddText(r.Error, "text-red-500")
}
