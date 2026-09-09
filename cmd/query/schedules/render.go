package schedules

import (
	"fmt"
	"strings"

	"github.com/flanksource/clicky/api"
)

// Columns is the schedule listing. It deliberately omits the nested query,
// reconcile, report and delivery blocks: a listing answers "what is scheduled,
// is it on, and what does it do", and rendering four nested documents per row
// answers none of those.
func (s Schedule) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		{Name: "Name", Style: "font-semibold"},
		{Name: "Schedule"},
		{Name: "Runs"},
		{Name: "Enabled"},
		{Name: "Report"},
		{Name: "Delivers"},
	}
}

func (s Schedule) Row() map[string]any {
	enabled := api.Text{Content: "paused", Style: "text-slate-500"}
	if s.Enabled {
		enabled = api.Text{Content: "enabled", Style: "text-green-600"}
	}

	report := "—"
	if s.Report != nil {
		report = s.Report.Format
	}

	return map[string]any{
		"Name":     s.Name,
		"Schedule": s.cronLabel(),
		"Runs":     s.target(),
		"Enabled":  enabled,
		"Report":   report,
		"Delivers": s.deliveryLabel(),
	}
}

// cronLabel shows the spec with its timezone, because "0 6 * * *" means
// different things in two places and the row is where that ambiguity bites.
func (s Schedule) cronLabel() string {
	if s.Timezone == "" {
		return s.Cron
	}
	return fmt.Sprintf("%s (%s)", s.Cron, s.Timezone)
}

// target names what the schedule runs, which is the one thing a reader needs to
// tell two schedules apart.
func (s Schedule) target() string {
	switch {
	case s.Query != nil:
		return "query " + s.Query.Profile
	case s.Reconcile != nil:
		return "reconcile " + s.Reconcile.Profile
	default:
		return "—"
	}
}

func (s Schedule) deliveryLabel() string {
	if len(s.Deliver) == 0 {
		return "—"
	}
	targets := make([]string, 0, len(s.Deliver))
	for _, delivery := range s.Deliver {
		targets = append(targets, delivery.Connection)
	}
	return strings.Join(targets, ", ")
}

// Columns is the run listing: identity, outcome, and the counts that say
// whether an outcome is trustworthy.
func (r Run) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		{Name: "ID"},
		{Name: "Schedule", Style: "font-semibold"},
		{Name: "Status"},
		{Name: "Tasks"},
		{Name: "Failed"},
		{Name: "Started", Type: "date"},
		{Name: "Artifact"},
	}
}

func (r Run) Row() map[string]any {
	failed := api.Text{Content: fmt.Sprintf("%d", r.Failed)}
	if r.Failed > 0 {
		failed.Style = "text-red-600"
	}

	artifact := "—"
	if r.ArtifactPath != "" {
		artifact = r.ArtifactPath
	}

	row := map[string]any{
		"ID":       r.ID,
		"Schedule": r.Schedule,
		"Status":   api.Text{Content: r.Status, Style: runStatusStyle(r.Status)},
		"Tasks":    r.Total,
		"Failed":   failed,
		"Artifact": artifact,
	}
	if r.StartedAt != nil {
		row["Started"] = *r.StartedAt
	}
	return row
}

func runStatusStyle(status string) string {
	switch status {
	case "success", "PASS":
		return "text-green-600"
	case "failed", "FAIL", "ERR":
		return "text-red-600"
	case "running", "pending":
		return "text-sky-600"
	default:
		return "text-slate-500"
	}
}
