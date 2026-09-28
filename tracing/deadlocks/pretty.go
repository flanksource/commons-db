package deadlocks

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/flanksource/clicky/api"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// timeLayout prints a deadlock's time to the millisecond, which is what tells
// a burst of deadlocks apart.
const timeLayout = "2006-01-02 15:04:05.000"

func shapeStyle(shape Shape) string {
	if shape == ShapeOther {
		return "text-muted"
	}
	return "text-orange-600 font-medium"
}

// Pretty renders the detail a `sql deadlocks get` prints: the headline, both
// participant and lock tables, and the cycle as one line per edge.
func (g Graph) Pretty() api.Text {
	text := api.Text{}.
		AddText(g.Timestamp.UTC().Format(timeLayout)+" UTC", "font-bold").
		AddText(" · ", "text-muted").
		AddText(g.Shape.Label(), shapeStyle(g.Shape)).
		AddText(" · ", "text-muted").
		AddText(g.Database, "text-blue-500").
		NewLine().
		AddText(g.Signature, "font-mono text-muted").
		NewLine().NewLine().
		AddText("Participants", "font-bold").NewLine()
	participants := make([]participantRow, len(g.Processes))
	for i, p := range g.Processes {
		participants[i] = participantRow(p)
	}
	resources := make([]resourceRow, len(g.Resources))
	for i, r := range g.Resources {
		resources[i] = resourceRow{Resource: r, processes: g.Processes}
	}
	children := []api.Textable{text, api.NewTableFrom(participants),
		api.Text{}.NewLine().AddText("Lock resources", "font-bold").NewLine(), api.NewTableFrom(resources),
		api.Text{}.NewLine().AddText("Cycle", "font-bold").NewLine(), g.cycle()}
	return api.Text{Children: children}
}

// cycle prints every wait in the graph as the sentence an operator reads it
// as: who waits for what, on which lock, behind whom.
func (g Graph) cycle() api.Text {
	text := api.Text{}
	for _, r := range g.Resources {
		for _, waiter := range r.Waiters {
			var holders []string
			for _, owner := range r.Owners {
				if owner.ProcessID == waiter.ProcessID {
					continue
				}
				verb := "held " + owner.Mode
				if !owner.Granted() {
					verb = "queued " + owner.Mode
				}
				holders = append(holders, verb+" by "+g.processLabel(owner.ProcessID))
			}
			text = text.AddText(g.processLabel(waiter.ProcessID), "font-medium").
				AddText(" wants "+waiter.Mode+" on ", "text-muted").
				AddText(r.Kind+" "+resourceTarget(r), "font-mono").
				AddText(", "+strings.Join(holders, ", "), "text-muted").
				NewLine()
		}
	}
	return text
}

func (g Graph) processLabel(id string) string {
	for _, p := range g.Processes {
		if p.ID == id {
			if p.Victim {
				return fmt.Sprintf("SPID %d (victim)", p.SPID)
			}
			return fmt.Sprintf("SPID %d", p.SPID)
		}
	}
	return id
}

func resourceTarget(r Resource) string {
	switch {
	case r.Object != "" && r.Index != "":
		return r.Object + "." + r.Index
	case r.Object != "":
		return r.Object
	case r.LockID != "":
		return r.LockID
	}
	return ""
}

type participantRow Process

func (participantRow) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("role").Label("Role").Build(),
		api.Column("spid").Label("SPID").Style("text-right").Build(),
		api.Column("host").Label("Host").Build(),
		api.Column("app").Label("App").Build(),
		api.Column("login").Label("Login").Build(),
		api.Column("waiting").Label("Waiting").Build(),
		api.Column("tranCount").Label("Tran").Style("text-right").Build(),
		api.Column("tranStarted").Label("Tran started (server time)").Build(),
		api.Column("wait").Label("Waited").Style("text-right").Build(),
		api.Column("waitingSince").Label("Waiting since (UTC)").Build(),
		api.Column("statement").Label("Running statement").MaxWidth(100).Build(),
	}
}

func (p participantRow) Row() map[string]any {
	role := api.Text{Content: "survivor", Style: "text-muted"}
	if p.Victim {
		role = api.Text{Content: "victim", Style: "text-red-600 font-medium"}
	}
	waitingSince := ""
	if p.WaitingSince != nil {
		waitingSince = p.WaitingSince.UTC().Format(timeLayout)
	}
	return map[string]any{
		"role": role, "spid": p.SPID, "host": p.Host, "app": p.ClientApp, "login": p.Login,
		"waiting": p.LockMode, "tranCount": p.TranCount, "tranStarted": p.LastTranStarted,
		"wait":         api.Human(time.Duration(p.WaitTimeMs)*time.Millisecond, "text-muted"),
		"waitingSince": waitingSince,
		"statement":    runningStatement(Process(p)),
	}
}

// runningStatement is the statement a participant was running, flagged when
// SQL Server's input buffer kept only its start. The flag leads, because the
// column clips a long statement.
func runningStatement(p Process) api.Text {
	statement := collapseWhitespace(p.Statement)
	if !p.InputBufferTruncated {
		return api.Text{Content: statement, Style: "text-muted"}
	}
	note := message.NewPrinter(language.English).Sprintf("(truncated by SQL Server: %d of %d characters) ",
		len(utf16.Encode([]rune(p.Statement))), p.StatementLength)
	return api.Text{}.AddText(note, "text-orange-600").AddText(statement, "text-muted")
}

type resourceRow struct {
	Resource
	processes []Process
}

func (resourceRow) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("kind").Label("Lock").Build(),
		api.Column("target").Label("Object.Index").Build(),
		api.Column("indexType").Label("Index type").Build(),
		api.Column("resolution").Label("Named by").Build(),
		api.Column("held").Label("Held").Build(),
		api.Column("wanted").Label("Wanted").Build(),
	}
}

func (r resourceRow) Row() map[string]any {
	return map[string]any{
		"kind": r.Kind, "target": resourceTarget(r.Resource), "indexType": string(r.IndexType),
		"resolution": string(r.Resolution),
		"held":       r.requests(r.Owners),
		"wanted":     r.requests(r.Waiters),
	}
}

func (r resourceRow) requests(requests []LockRequest) string {
	parts := make([]string, len(requests))
	for i, request := range requests {
		label := request.ProcessID
		for _, p := range r.processes {
			if p.ID == request.ProcessID {
				label = fmt.Sprintf("SPID %d", p.SPID)
			}
		}
		suffix := ""
		if !request.Granted() {
			suffix = " (waiting)"
		}
		parts[i] = strings.TrimSpace(request.Mode+" "+label) + suffix
	}
	return strings.Join(parts, ", ")
}
