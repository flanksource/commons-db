package sqlactivity

import (
	"fmt"
	"time"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/commons-db/query/sqltext"
)

func (s Sample) Pretty() api.Text {
	elapsed := time.Duration(s.ElapsedMs * float64(time.Millisecond))
	t := clicky.Text(s.Database, "text-blue-500").Space().Append(fmt.Sprintf("#%d", s.SessionID), "text-muted").
		Space().Append(s.State, "text-muted")
	if s.WaitType != "" {
		t = t.Space().Append(s.WaitType, "text-orange-500")
	}
	t = t.Space().Add(api.Human(elapsed, sqltext.DurationStyle(elapsed)))
	if s.Blocked {
		t = t.Space().Append(fmt.Sprintf("blocked by #%d", s.BlockedBy), "text-red-500")
	}
	return t.Space().Append(s.SQL, "font-mono max-w-[200ch]")
}

func (s Sample) PrettyFull() api.Textable {
	return s.Pretty().NewLine().Add(clicky.CodeBlock("sql", s.SQL)).NewLine().Add(clicky.Map(map[string]any{
		"Engine": s.Engine, "User": s.User, "Application": s.Application, "Client host": s.ClientHost,
		"Command": s.Command, "Wait resource": s.WaitResource,
		"Waited":      api.Human(time.Duration(s.WaitMs * float64(time.Millisecond))),
		"Transaction": api.Human(time.Duration(s.TransactionMs * float64(time.Millisecond))),
		"CPU":         api.Human(time.Duration(s.CPUMs * float64(time.Millisecond))),
		"Reads":       s.LogicalReads, "Physical reads": s.Reads, "Writes": s.Writes,
		"Memory": api.HumanizeBytes(s.MemoryBytes), "Open transactions": s.OpenTransactions, "Query id": s.QueryID,
	}))
}
