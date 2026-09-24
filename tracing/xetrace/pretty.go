package xetrace

import (
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

// TraceResult is the final value returned by the `sql trace` command. It is
// intentionally serializable as JSON (for --format json) and renders as a
// single-line summary in text mode. The per-event table is streamed to stdout
// during collection by the command handler, not rendered from here.
type TraceResult struct {
	SessionName string         `json:"session_name"`
	Database    string         `json:"database"`
	StartedAt   time.Time      `json:"started_at"`
	StoppedAt   time.Time      `json:"stopped_at"`
	Duration    time.Duration  `json:"duration"`
	Events      []Event        `json:"events"`
	Replays     []ReplayResult `json:"replays,omitempty"`
	Error       string         `json:"error,omitempty"`
}

// Pretty renders a one-line summary followed by a table of the captured
// events. The JSON/YAML formats bypass Pretty and serialize the exported
// fields instead.
func (r TraceResult) Pretty() api.Text {
	summary := api.Text{}.
		Add(icons.SQL).Space().
		AddText(fmt.Sprintf("%d events", len(r.Events)), "font-bold").
		AddText(" captured in ", "text-muted").
		Add(api.Human(r.Duration, "text-muted")).
		AddText(" from ", "text-muted").
		AddText(displayDatabase(r.Database), "text-blue-500")

	if len(r.Events) == 0 {
		return summary
	}

	children := []api.Textable{summary, api.Text{Content: "\n"}, r.body()}
	if len(r.Replays) > 0 {
		// Per-replay blocks are streamed to stderr inline next to their
		// corresponding trace line, so the final summary just needs the
		// aggregate counts to close the section. Programmatic consumers
		// still see every detail via the JSON/YAML serialization of
		// TraceResult.Replays.
		children = append(children, api.Text{Content: "\n"}, replaySummary(r.Replays))
	}
	return api.Text{Children: children}
}

// body renders the captured events, as a tree when a procedure's inner
// statements were captured and as the flat table otherwise.
//
// The flat table stays the default on purpose: a tree row cannot carry the
// duration/CPU/reads columns, and without sp_statement_completed there is
// nothing to nest, so an ordinary trace renders exactly as it always has.
func (r TraceResult) body() api.Textable {
	events := Nest(r.Events)
	if !HasChildren(events) {
		return api.NewTableFrom(r.Events)
	}
	opts := StreamLineOptions{ShowDatabase: r.Database == ""}
	root := api.TextTree{}
	for _, e := range events {
		root.Children = append(root.Children, eventTree(e, opts))
	}
	return root
}

// eventTree renders one event and, beneath it, the inner statements attributed
// to it by Nest.
func eventTree(e Event, opts StreamLineOptions) api.TextTree {
	node := api.TextTree{Node: StreamLine(e, opts)}
	for _, c := range e.Children {
		node.Children = append(node.Children, eventTree(c, opts))
	}
	return node
}

// replaySummary renders a single-line "N replayed / M skipped" header that
// precedes the per-replay blocks.
func replaySummary(replays []ReplayResult) api.Text {
	var ran, skipped, failed int
	for _, r := range replays {
		switch {
		case !r.Eligible:
			skipped++
		case r.Error != "":
			failed++
			ran++
		default:
			ran++
		}
	}
	return api.Text{}.
		AddText("replay: ", "font-bold").
		AddText(fmt.Sprintf("%d ran", ran), "text-green-500").
		AddText(", ", "text-muted").
		AddText(fmt.Sprintf("%d skipped", skipped), "text-muted").
		AddText(", ", "text-muted").
		AddText(fmt.Sprintf("%d failed", failed), "text-red-500")
}

// Pretty renders a single replay result as a compact block: the
// (truncated) SQL on the first line, row count + duration on the second,
// and up to 3 first rows as `key=value` lines below.
func (r ReplayResult) Pretty() api.Text {
	head := api.Text{}.
		AddText("→ ", "text-muted").
		Add(api.CodeBlock("text/x-sql", firstN(r.SQL, 200))).
		Space().
		Add(api.Human(r.Duration, durationStyle(r.Duration)))

	if r.Error != "" {
		return api.Text{Children: []api.Textable{
			head,
			api.Text{Content: "\n"},
			api.Text{}.AddText("  error: ", "text-red-500 font-bold").AddText(r.Error, "text-red-500"),
		}}
	}

	countLabel := fmt.Sprintf("  %d rows", r.RowCount)
	if r.RowCountTruncated {
		countLabel += " (truncated)"
	}
	rowsLine := api.Text{}.AddText(countLabel, "text-muted")

	children := []api.Textable{head, api.Text{Content: "\n"}, rowsLine}
	for i, row := range r.FirstRows {
		prefix := fmt.Sprintf("  [%d] ", i+1)
		children = append(children, api.Text{Content: "\n"},
			api.Text{}.AddText(prefix, "text-muted").AddText(formatRow(row), ""))
	}
	return api.Text{Children: children}
}

// formatRow renders a row map as `col=val col2=val2 …`, keeping only the
// first ~6 columns so wide tables don't blow out the terminal.
func formatRow(row map[string]any) string {
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	// Deterministic order: rely on the column order the driver gave us by
	// sorting alphabetically — driver order would require keeping a slice
	// of columns alongside the map, which adds clutter for minimal gain.
	sortStrings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, row[k]))
		if len(parts) >= 6 {
			parts = append(parts, "…")
			break
		}
	}
	return strings.Join(parts, " ")
}

// sortStrings is a local inline sort to avoid adding the "sort" import
// just for one call site. Small n, insertion sort is fine.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// eventMarker returns a styled single-cell glyph that identifies the event
// kind. Uses double-struck letters from the Mathematical Alphanumeric
// Symbols block (𝕊 ℝ 𝔹 𝔼) because they are distinctive, semantically
// close to the event names, and — unlike emoji — render as a single
// terminal cell in monospace fonts, keeping column widths stable.
func eventMarker(e Event) api.Text {
	glyph, style := eventMarkerGlyph(e.Name)
	return api.Text{Content: glyph, Style: style}
}

func eventMarkerGlyph(name string) (string, string) {
	switch name {
	case EventSQLStatementCompleted:
		return "𝕊", "text-green-500 font-bold"
	case EventRPCCompleted:
		return "ℝ", "text-blue-500 font-bold"
	case EventSQLBatchCompleted:
		return "𝔹", "text-cyan-500 font-bold"
	case EventErrorReported:
		return "𝔼", "text-red-500 font-bold"
	case EventSPStatementCompleted:
		return "ℙ", "text-purple-500 font-bold"
	default:
		return "?", "text-muted"
	}
}

func durationStyle(d time.Duration) string {
	switch {
	case d >= 1*time.Second:
		return "text-red-500 font-bold"
	case d >= 100*time.Millisecond:
		return "text-red-500"
	case d >= 10*time.Millisecond:
		return "text-orange-500"
	case d >= 1*time.Millisecond:
		return "text-yellow-500"
	default:
		return "text-muted"
	}
}

// metricStyle returns the style applied to a single bracketed metric value.
// Values above 100 are flagged in orange; everything else is muted.
func metricStyle(n int64) string {
	if n > 100 {
		return "text-orange-500"
	}
	return "text-muted"
}

// Columns implements api.TableProvider on Event.
func (Event) Columns() []api.ColumnDef {
	return []api.ColumnDef{
		api.Column("time").Label("Time").Build(),
		api.Column("event").Label("Event").Build(),
		api.Column("duration").Label("Duration").Build(),
		api.Column("cpu").Label("CPU").Build(),
		api.Column("reads").Label("Reads").Build(),
		api.Column("rows").Label("Rows").Build(),
		api.Column("db").Label("DB").Build(),
		api.Column("sid").Label("SID").Build(),
		api.Column("user").Label("User").Build(),
		api.Column("statement").Label("Statement").MaxWidth(120).Build(),
	}
}

// Row implements api.TableProvider on Event.
func (e Event) Row() map[string]any {
	row := map[string]any{
		"time":  e.Timestamp.Format("15:04:05.000"),
		"event": api.Text{}.Add(eventMarker(e)).Space().AddText(shortEvent(e.Name)),
		"db":    e.DatabaseName,
		"sid":   e.SessionID,
		"user":  e.Username,
	}

	if e.Duration > 0 {
		row["duration"] = api.Human(e.Duration, durationStyle(e.Duration))
	}
	if e.CPUTime > 0 {
		row["cpu"] = api.Human(e.CPUTime, "text-muted")
	}
	if e.LogicalReads > 0 {
		row["reads"] = api.HumanNumber(e.LogicalReads, metricStyle(e.LogicalReads))
	}
	if e.RowCount > 0 {
		row["rows"] = api.HumanNumber(e.RowCount, "text-muted")
	}

	row["statement"] = statementCell(e, "")
	return row
}

// statementCell returns the Text/Code used for the statement column. For
// statement/rpc/batch events it renders as a SQL CodeBlock so the ANSI/HTML
// output gets syntax highlighting; for error events it renders the error
// message in red. widthClass, when non-empty, is passed to the underlying
// CodeBlock as a Tailwind class — e.g. "max-w-[200ch]" for stream lines.
//
// Statements that came in as sp_prepexec/sp_executesql scaffolding are
// rewritten via UnwrapRPC before rendering so the user sees the real call
// with parameters inlined. The original (raw) text stays accessible via
// RowDetail for debugging.
func statementCell(e Event, widthClass string) api.Textable {
	if e.SQL != "" {
		if widthClass != "" {
			return api.CodeBlock("text/x-sql", e.SQL, widthClass)
		}
		return api.CodeBlock("text/x-sql", e.SQL)
	}
	if e.ErrorMessage != "" {
		return api.Text{}.
			AddText(fmt.Sprintf("[%d] ", e.ErrorNumber), "text-red-500 font-bold").
			AddText(e.ErrorMessage, "text-red-500")
	}
	return api.Text{}
}

// RowDetail implements api.DetailProvider: the expanded row shows the full
// statement (as a syntax-highlighted SQL block) plus metadata and — for error
// events — the full error payload.
func (e Event) RowDetail() api.Textable {
	t := api.Text{}
	hasContent := false

	if e.SQL != "" {
		hasContent = true
		t = t.AddText("Statement", "font-bold text-muted").NewLine().
			Add(statementCell(e, ""))

		if e.ParamsUnavailable {
			t = t.NewLine().Add(paramsUnavailableNote())
		}

		if e.Statement != "" && e.Statement != e.SQL {
			t = t.NewLine().
				AddText("Raw RPC", "font-bold text-muted").NewLine().
				Add(api.CodeBlock("text/x-sql", collapseWhitespace(strings.TrimSpace(e.Statement))))
		}
	}

	if e.ErrorMessage != "" {
		if hasContent {
			t = t.NewLine()
		}
		hasContent = true
		t = t.AddText("Error", "font-bold text-muted").NewLine().
			AddText(fmt.Sprintf("[%d] ", e.ErrorNumber), "text-red-500 font-bold").
			AddText(e.ErrorMessage)
	}

	metrics := []api.KeyValuePair{}
	metrics = appendKV(metrics, "duration", humanOrEmpty(e.Duration))
	metrics = appendKV(metrics, "cpu_time", humanOrEmpty(e.CPUTime))
	metrics = appendKV(metrics, "logical_reads", intOrEmpty(e.LogicalReads))
	metrics = appendKV(metrics, "physical_reads", intOrEmpty(e.PhysicalReads))
	metrics = appendKV(metrics, "writes", intOrEmpty(e.Writes))
	metrics = appendKV(metrics, "row_count", intOrEmpty(e.RowCount))
	metrics = appendKV(metrics, "object_name", e.ObjectName)
	metrics = appendKV(metrics, "object_id", intOrEmpty(e.ObjectID))
	metrics = appendKV(metrics, "database_name", e.DatabaseName)
	metrics = appendKV(metrics, "client_app_name", e.ClientApp)
	metrics = appendKV(metrics, "client_hostname", e.ClientHost)
	metrics = appendKV(metrics, "username", e.Username)
	metrics = appendKV(metrics, "session_id", intOrEmpty(int64(e.SessionID)))

	if len(metrics) > 0 {
		if hasContent {
			t = t.NewLine()
		}
		hasContent = true
		t = t.AddText("Metrics", "font-bold text-muted").NewLine().
			Add(api.DescriptionList{Items: metrics})
	}

	if !hasContent {
		return nil
	}
	return t
}

func appendKV(items []api.KeyValuePair, key, value string) []api.KeyValuePair {
	if value == "" {
		return items
	}
	return append(items, api.KeyValuePair{Key: key, Value: value})
}

func humanOrEmpty(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
}

func intOrEmpty(n int64) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d", n)
}

func displayDatabase(name string) string {
	if name == "" {
		return "<all databases>"
	}
	return name
}

// StreamLineOptions controls how StreamLine formats a single live event.
type StreamLineOptions struct {
	// ShowDatabase adds a "db=<name>" segment. Only set when the trace
	// session is instance-wide (--database all); scoped sessions would
	// otherwise repeat the same value on every line.
	ShowDatabase bool
	// Full removes the max-w-[200ch] cap on the statement cell so long
	// statements wrap naturally instead of being elided.
	Full bool
}

// StreamLine builds a styled single-line representation of an event for live
// streaming to stderr during the poll loop. The final summary table is built
// separately via Event.Columns/Row — this function is only for the per-event
// live stream.
func StreamLine(e Event, opts StreamLineOptions) api.Text {
	widthClass := "max-w-[200ch]"
	if opts.Full {
		widthClass = ""
	}

	t := api.Text{}.
		AddText(e.Timestamp.Format("15:04:05.000"), "text-muted").Space().
		Add(eventMarker(e)).Space().
		Add(statementCell(e, widthClass)).Space().
		Add(api.Human(e.Duration, durationStyle(e.Duration)))

	if e.ParamsUnavailable {
		t = t.Space().Add(paramsUnavailableNote())
	}

	if bracket := metricsBracket(e); bracket != nil {
		t = t.Space().Add(bracket)
	}

	if opts.ShowDatabase && e.DatabaseName != "" {
		t = t.Space().
			AddText("db=", "text-muted").
			AddText(e.DatabaseName, "text-blue-500")
	}

	if e.Username != "" {
		t = t.Space().AddText(e.Username, "text-muted")
	}

	return t
}

// metricsBracket returns "(reads: 42, writes: 1, rows: 3)" when any of the
// tracked metrics is non-zero, otherwise nil. Values above 100 are
// highlighted orange via metricStyle; everything else is muted. `rows` is
// SQL Server's row_count column — rows returned for SELECT, rows affected
// for INSERT/UPDATE/DELETE.
func metricsBracket(e Event) api.Textable {
	type kv struct {
		label string
		value int64
	}
	metrics := []kv{
		{"reads", e.LogicalReads + e.PhysicalReads},
		{"writes", e.Writes},
		{"rows", e.RowCount},
	}

	var present []kv
	for _, m := range metrics {
		if m.value > 0 {
			present = append(present, m)
		}
	}
	if len(present) == 0 {
		return nil
	}

	t := api.Text{}.AddText("(", "text-muted")
	for i, m := range present {
		if i > 0 {
			t = t.AddText(", ", "text-muted")
		}
		t = t.AddText(m.label+": ", "text-muted").
			AddText(fmt.Sprintf("%d", m.value), metricStyle(m.value))
	}
	t = t.AddText(")", "text-muted")
	return t
}

func shortEvent(name string) string {
	switch name {
	case EventSQLStatementCompleted:
		return "stmt"
	case EventRPCCompleted:
		return "rpc"
	case EventSQLBatchCompleted:
		return "batch"
	case EventErrorReported:
		return "error"
	case EventSPStatementCompleted:
		return "sp"
	default:
		return name
	}
}

// paramsUnavailableNote marks a statement whose placeholders were never bound
// to values in the trace, so a reader does not take `?` / `@P0` for the literal
// call that ran.
func paramsUnavailableNote() api.Text {
	return api.Text{}.AddText("(params not captured)", "text-yellow-600")
}

// firstN truncates s to at most n bytes, for the summary blocks that show a
// statement rather than the whole of it.
func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
