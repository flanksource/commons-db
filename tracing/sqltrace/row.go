package sqltrace

import (
	"time"

	"github.com/flanksource/clicky/api"

	"github.com/flanksource/commons-db/tracing/deadlocks"
	"github.com/flanksource/commons-db/tracing/xetrace"
)

// RowKind is the record kind a capture's stream holds: one EventRow per
// captured event.
const RowKind = "sql_xevent"

// EventRow is one captured event as a capture stores it: its metrics in
// milliseconds, its identity and text, and, for a deadlock report, the decoded
// graph.
type EventRow struct {
	Name          string    `json:"name" pretty:"label=Event" filter:"terms"`
	StatementType string    `json:"statementType" pretty:"label=Type" filter:"terms"`
	Timestamp     time.Time `json:"timestamp" pretty:"label=Time" sort:"timestamp"`
	DurationMs    float64   `json:"durationMs" pretty:"label=Duration,type=duration,unit=ms" sort:"durationMs"`
	CPUMs         float64   `json:"cpuMs" pretty:"label=CPU,type=duration,unit=ms" sort:"cpuMs"`
	LogicalReads  int64     `json:"logicalReads" pretty:"label=Logical reads" sort:"logicalReads"`
	PhysicalReads int64     `json:"physicalReads" pretty:"label=Physical reads" sort:"physicalReads"`
	Writes        int64     `json:"writes" sort:"writes"`
	RowCount      int64     `json:"rowCount" pretty:"label=Rows" sort:"rowCount"`
	Database      string    `json:"database" filter:"terms"`
	ClientApp     string    `json:"clientApp" pretty:"label=App" filter:"terms"`
	ClientHost    string    `json:"clientHost" pretty:"label=Host" filter:"terms"`
	Username      string    `json:"username" pretty:"label=User" filter:"terms"`
	SessionID     int       `json:"sessionId" pretty:"label=Session" filter:"exact"`

	SQL          string   `json:"sql" pretty:"label=SQL" filter:"text"`
	RawStatement string   `json:"rawStatement" pretty:"hide"`
	Tables       []string `json:"tables"`

	ErrorNumber       int              `json:"errorNumber" pretty:"label=Error"`
	ErrorMessage      string           `json:"errorMessage" pretty:"label=Error message" filter:"text"`
	ObjectName        string           `json:"objectName" pretty:"label=Object" filter:"terms"`
	ObjectType        string           `json:"objectType" pretty:"label=Object type" filter:"terms"`
	ParamsUnavailable bool             `json:"paramsUnavailable" pretty:"label=Params unavailable"`
	AdditionalFields  map[string]any   `json:"additionalFields,omitempty" pretty:"hide"`
	Deadlock          *deadlocks.Graph `json:"deadlock,omitempty" pretty:"hide"`

	ActivityID  string     `json:"activityId" pretty:"hide"`
	ActivitySeq int        `json:"activitySeq" pretty:"hide"`
	Children    []EventRow `json:"children" pretty:"hide"`
}

// FromEvent is the row a capture stores for event, with its nested statements.
func FromEvent(event xetrace.Event) EventRow {
	children := make([]EventRow, len(event.Children))
	for index, child := range event.Children {
		children[index] = FromEvent(child)
	}
	return EventRow{
		Name: event.Name, StatementType: string(event.StatementType), Timestamp: event.Timestamp,
		DurationMs: milliseconds(event.Duration), CPUMs: milliseconds(event.CPUTime),
		LogicalReads: event.LogicalReads, PhysicalReads: event.PhysicalReads, Writes: event.Writes, RowCount: event.RowCount,
		Database: event.DatabaseName, ClientApp: event.ClientApp, ClientHost: event.ClientHost,
		Username: event.Username, SessionID: event.SessionID,
		SQL: event.SQL, RawStatement: event.Statement, Tables: append([]string{}, event.Tables...),
		ErrorNumber: event.ErrorNumber, ErrorMessage: event.ErrorMessage,
		ObjectName: event.ObjectName, ObjectType: event.ObjectType, ParamsUnavailable: event.ParamsUnavailable,
		AdditionalFields: event.AdditionalFields,
		ActivityID:       event.ActivityID, ActivitySeq: event.ActivitySeq, Children: children,
	}
}

func (EventRow) Columns() []api.ColumnDef {
	numeric := func(name, label string) api.ColumnDef {
		return api.Column(name).Label(label).Style("text-right").Type("number").FilterKey("filter." + name).Build()
	}
	duration := func(name, label string) api.ColumnDef {
		return api.Column(name).Label(label).Style("text-right").Type("duration").Unit("ms").FilterKey("filter." + name).Build()
	}
	return []api.ColumnDef{
		api.Column("timestamp").Label("Time").Kind("timestamp").Build(),
		api.Column("name").Label("Event").Build(),
		api.Column("sql").Label("SQL").MinWidthPixels(360).MaxWidthPixels(720).Build(),
		api.Column("statementType").Label("Type").Build(),
		api.Column("tables").Label("Tables").Kind("tags").Build(),
		duration("durationMs", "Duration"),
		api.Column("database").Label("DB").Build(),
		api.Column("username").Label("User").Build(),
		api.Column("sessionId").Label("SID").Style("text-right").Build(),
		api.Column("clientHost").Label("Host").DefaultHidden().Build(),
		api.Column("clientApp").Label("App").DefaultHidden().Build(),
		api.Column("objectName").Label("Object").DefaultHidden().Build(),
		api.Column("objectType").Label("Object type").DefaultHidden().Build(),
		duration("cpuMs", "CPU"),
		numeric("logicalReads", "Reads"),
		numeric("physicalReads", "Physical reads"),
		numeric("writes", "Writes"),
		numeric("rowCount", "Rows"),
		api.Column("rawStatement").Hidden().Build(),
		api.Column("errorNumber").Hidden().Build(),
		api.Column("errorMessage").Hidden().Build(),
		api.Column("paramsUnavailable").Hidden().Build(),
		api.Column("additionalFields").Kind("json").Hidden().Build(),
		api.Column("deadlock").Kind("json").Hidden().Build(),
		api.Column("activityId").Hidden().Build(),
		api.Column("activitySeq").Hidden().Build(),
		api.Column("children").Hidden().Build(),
	}
}

func (r EventRow) Row() map[string]any {
	event := xetrace.Event{
		Name: r.Name, Timestamp: r.Timestamp,
		Duration:     time.Duration(r.DurationMs * float64(time.Millisecond)),
		CPUTime:      time.Duration(r.CPUMs * float64(time.Millisecond)),
		LogicalReads: r.LogicalReads, PhysicalReads: r.PhysicalReads,
		Writes: r.Writes, RowCount: r.RowCount, DatabaseName: r.Database,
		ClientApp: r.ClientApp, ClientHost: r.ClientHost, Username: r.Username,
		SessionID: r.SessionID, Statement: r.RawStatement, SQL: r.SQL,
		StatementType: xetrace.StatementType(r.StatementType), Tables: r.Tables,
		ErrorNumber: r.ErrorNumber, ErrorMessage: r.ErrorMessage,
		ObjectName: r.ObjectName, ObjectType: r.ObjectType, ParamsUnavailable: r.ParamsUnavailable,
		AdditionalFields: r.AdditionalFields,
		ActivityID:       r.ActivityID, ActivitySeq: r.ActivitySeq,
	}
	display := event.Row()
	row := map[string]any{
		"timestamp":    r.Timestamp,
		"name":         api.TableCell{Value: display["event"], FilterValue: r.Name},
		"durationMs":   api.TableCell{Value: display["duration"], FilterValue: r.DurationMs},
		"cpuMs":        api.TableCell{Value: display["cpu"], FilterValue: r.CPUMs},
		"logicalReads": api.TableCell{Value: display["reads"], FilterValue: r.LogicalReads},
		"physicalReads": api.TableCell{
			Value: humanCount(r.PhysicalReads), FilterValue: r.PhysicalReads,
		},
		"writes":   api.TableCell{Value: humanCount(r.Writes), FilterValue: r.Writes},
		"rowCount": api.TableCell{Value: display["rows"], FilterValue: r.RowCount},
		"database": r.Database, "sessionId": r.SessionID, "username": r.Username,
		"tables":        r.Tables,
		"sql":           api.TableCell{Value: display["statement"], FilterValue: r.SQL},
		"statementType": r.StatementType,
		"clientApp":     r.ClientApp, "clientHost": r.ClientHost, "rawStatement": r.RawStatement,
		"errorNumber": r.ErrorNumber, "errorMessage": r.ErrorMessage, "objectName": r.ObjectName,
		"objectType": r.ObjectType, "paramsUnavailable": r.ParamsUnavailable, "activityId": r.ActivityID,
		"activitySeq": r.ActivitySeq, "children": r.Children, "additionalFields": r.AdditionalFields,
	}
	if r.Deadlock != nil {
		row["deadlock"] = r.Deadlock
	}
	return row
}

func humanCount(value int64) api.Text {
	if value == 0 {
		return api.Text{}
	}
	return api.HumanNumber(value, "text-muted")
}

func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
