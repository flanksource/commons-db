package sessions

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"

	"github.com/flanksource/commons-db/query"
)

const clickyTableMediaType = "application/json+clicky"

// sessionRow presents a session as a row of the sessions table.
type sessionRow query.SessionInfo

// sessionStateColors are the badge colours of each state.
var sessionStateColors = map[query.SessionState][2]string{
	query.SessionStarting:    {"bg-sky-500/10", "text-sky-700"},
	query.SessionRunning:     {"bg-sky-500/10", "text-sky-700"},
	query.SessionStopping:    {"bg-yellow-500/10", "text-yellow-700"},
	query.SessionCompleted:   {"bg-emerald-500/10", "text-emerald-700"},
	query.SessionStopped:     {"bg-slate-500/10", "text-slate-700"},
	query.SessionFailed:      {"bg-red-500/10", "text-red-700"},
	query.SessionInterrupted: {"bg-yellow-500/10", "text-yellow-700"},
}

func (sessionRow) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("_id").Hidden().Build(),
		api.Column("startedAt").Label("Started").Kind("timestamp").SortKey("startedAt").Build(),
		api.Column("session").Label("Session").MinWidthPixels(240).Build(),
		api.Column("profile").Label("Profile").FilterKey("profile").SortKey("profile").Build(),
		api.Column("target").Label("Target").FilterKey("label.target").Build(),
		api.Column("state").Label("State").FilterKey("state").SortKey("state").Build(),
		api.Column("eventCount").Label("Events").SortKey("eventCount").Build(),
		api.Column("stoppedAt").Label("Stopped").Kind("timestamp").SortKey("stoppedAt").Build(),
		api.Column("origin").Label("Origin").FilterKey("label.origin").Build(),
		api.Column("principal").Label("Principal").FilterKey("principal").SortKey("principal").Build(),
		api.Column("lineage").Label("Lineage").Build(),
		api.Column("labels").Label("Labels").Kind("tags").Build(),
		api.Column("owner").Label("Owner").Build(),
	}
}

func (s sessionRow) Row() map[string]any {
	colors := sessionStateColors[s.State]
	state := string(s.State)
	if s.Unresponsive {
		state += " · unresponsive"
	}
	label := s.Labels["label"]
	if label == "" {
		label = shortSessionID(s.ID)
	}
	labels := api.DescriptionList{}
	for _, key := range slices.Sorted(maps.Keys(s.Labels)) {
		if key != "label" {
			labels.Items = append(labels.Items, api.KeyValuePair{Key: key, Value: s.Labels[key]})
		}
	}
	stopped := ""
	if s.StoppedAt != nil {
		stopped = s.StoppedAt.Format(time.RFC3339Nano)
	}
	return map[string]any{
		"_id":        api.Text{Content: s.ID},
		"state":      api.TableCell{Value: api.LabelBadge{Value: state, Color: colors[0], TextColor: colors[1], Shape: "pill"}, FilterValue: string(s.State)},
		"session":    api.Text{Content: label},
		"profile":    api.Text{Content: s.Profile},
		"target":     api.Text{Content: s.Labels["target"]},
		"origin":     api.Text{Content: s.Labels["origin"]},
		"principal":  api.Text{Content: s.Principal},
		"startedAt":  api.Text{Content: s.StartedAt.Format(time.RFC3339Nano)},
		"stoppedAt":  api.Text{Content: stopped},
		"eventCount": s.EventCount,
		"lineage":    api.Text{Content: sessionLineage(query.SessionInfo(s))},
		"labels":     labels,
		"owner":      api.Text{Content: fmt.Sprintf("%s · pid %d", s.Owner.Host, s.Owner.PID)},
	}
}

func shortSessionID(id string) string { return id[:min(len(id), 8)] }

func sessionLineage(info query.SessionInfo) string {
	var parts []string
	if info.RestartOf != "" {
		parts = append(parts, "restart of "+shortSessionID(info.RestartOf))
	}
	if len(info.RestartedAs) > 0 {
		short := make([]string, len(info.RestartedAs))
		for i, id := range info.RestartedAs {
			short[i] = shortSessionID(id)
		}
		parts = append(parts, "restarted as "+strings.Join(short, ", "))
	}
	return strings.Join(parts, " · ")
}

// writeSessionTable serves infos as the clicky table document.
func writeSessionTable(w http.ResponseWriter, infos []query.SessionInfo) {
	rows := make([]sessionRow, len(infos))
	for i, info := range infos {
		rows[i] = sessionRow(info)
	}
	document, err := clicky.Format(api.NewTableFrom(rows), clicky.FormatOptions{Format: query.ClickyPageFormat})
	if err != nil {
		http.Error(w, fmt.Sprintf("render sessions table: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", clickyTableMediaType)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_, _ = w.Write([]byte(document))
}
