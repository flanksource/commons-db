package sqlactivity

import (
	"github.com/flanksource/clicky/api"
)

func (Sample) Columns() []api.ColumnDef {
	columns := []api.ColumnDef{
		api.Column("sessionId").Label("Session").Type("number").Build(),
		api.Column("state").Label("State").Build(),
		api.Column("waitType").Label("Wait").Build(),
		api.Column("elapsedMs").Label("Elapsed").Type("duration").Unit("ms").Build(),
		api.Column("blockedBy").Label("Blocker").Type("number").Build(),
		api.Column("sql").Label("SQL").Build(),
	}
	for i := range columns {
		switch columns[i].Name {
		case "sessionId", "blockedBy":
			columns[i].MaxWidth = 10
		case "state", "waitType":
			columns[i].MaxWidth = 20
		case "elapsedMs":
			columns[i].MaxWidth = 12
		case "sql":
			columns[i].MaxWidth = 80
		}
	}
	return columns
}

func (s Sample) Row() map[string]any {
	return map[string]any{"sessionId": s.SessionID, "state": s.State, "waitType": s.WaitType, "elapsedMs": s.ElapsedMs, "blockedBy": s.BlockedBy, "sql": s.SQL}
}
func (s Sample) RowDetail() api.Textable { return s.PrettyFull() }
