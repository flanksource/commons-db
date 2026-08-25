package query

import (
	"fmt"
	"time"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
)

// timeDiffAlarmThreshold is the lag past which the time-diff cell turns red. It
// is a display cue only — nothing in the engine treats a late arrival as an
// error, because "late" is profile-specific.
const timeDiffAlarmThreshold = 5 * time.Second

// reconcileFixedColumns are the join's own columns, emitted before either
// profile's columns.
var reconcileFixedColumns = []api.ColumnDef{
	api.Column("key").Label("Key").Style("font-bold").Build(),
	api.Column("status").Label("Status").Build(),
	api.Column("source_time").Label("Source Time").Build(),
	api.Column("dest_time").Label("Dest Time").Build(),
	api.Column("time_diff").Label("Time Diff").Build(),
}

// Columns returns the joined column schema: the fixed join columns, then each
// side's visible profile columns under a side-qualified key.
func (r *ReconcileResult) Columns() []api.ColumnDef {
	columns := append([]api.ColumnDef{}, reconcileFixedColumns...)
	columns = append(columns, r.sideColumns(true)...)
	columns = append(columns, r.sideColumns(false)...)
	return columns
}

func (r *ReconcileResult) sideColumns(isSource bool) []api.ColumnDef {
	profile := r.DestProfile
	if isSource {
		profile = r.SourceProfile
	}
	defs := profile.Columns
	if len(defs) == 0 {
		defs = deriveColumns(r.sideRows(isSource))
	}
	columns := make([]api.ColumnDef, 0, len(defs))
	for _, def := range defs {
		if def.Hidden {
			continue
		}
		key := r.sideColumnKey(def.Name, isSource)
		label := def.Label
		if label == "" {
			label = def.Name
		}
		columns = append(columns, api.ColumnDef{
			Name:     key,
			Label:    fmt.Sprintf("%s %s", sideLabel(isSource), label),
			Kind:     string(def.Kind),
			Type:     string(def.Type),
			Format:   def.clickyFormat(),
			Unit:     def.Unit,
			MaxWidth: def.Width,
		})
	}
	return columns
}

func (r *ReconcileResult) sideRows(isSource bool) []Row {
	rows := make([]Row, 0, len(r.Rows))
	for _, row := range r.Rows {
		side := row.Dest
		if isSource {
			side = row.Source
		}
		if side != nil {
			rows = append(rows, side)
		}
	}
	return rows
}

// sideColumnKey namespaces a profile column so the two sides never collide.
// Reconciling a profile against itself is a legitimate case (the same query
// against two connections), so the side is always part of the key when the
// profile names match.
func (r *ReconcileResult) sideColumnKey(column string, isSource bool) string {
	if r.SourceProfile.Name == r.DestProfile.Name {
		return fmt.Sprintf("%s_%s_%s", r.SourceProfile.Name, sideLabel(isSource), column)
	}
	if isSource {
		return fmt.Sprintf("%s_%s", r.SourceProfile.Name, column)
	}
	return fmt.Sprintf("%s_%s", r.DestProfile.Name, column)
}

func sideLabel(isSource bool) string {
	if isSource {
		return "src"
	}
	return "dest"
}

// Flatten renders the result as plain Rows keyed by Columns(), so a reconcile
// can be handed to anything that consumes a Result.
func (r *ReconcileResult) Flatten() []Row {
	rows := make([]Row, 0, len(r.Rows))
	for _, row := range r.Rows {
		rows = append(rows, r.flattenRow(row))
	}
	return rows
}

func (r *ReconcileResult) flattenRow(row ReconcileRow) Row {
	out := Row{
		"key":         renderReconcileKey(row),
		"status":      renderReconcileStatus(row.Status),
		"source_time": renderReconcileTime(row.SourceTime),
		"dest_time":   renderReconcileTime(row.DestTime),
		"time_diff":   renderTimeDiff(row.TimeDiff),
	}
	r.copySideCells(out, row.Source, true)
	r.copySideCells(out, row.Dest, false)
	return out
}

// copySideCells writes one side's cells under their side-qualified keys, filling
// every declared column so a missing side renders as blanks rather than as a
// ragged row.
func (r *ReconcileResult) copySideCells(out Row, side Row, isSource bool) {
	for _, column := range r.sideColumns(isSource) {
		out[column.Name] = ""
	}
	if side == nil {
		return
	}
	profile := r.DestProfile
	if isSource {
		profile = r.SourceProfile
	}
	defs := profile.Columns
	if len(defs) == 0 {
		defs = deriveColumns(r.sideRows(isSource))
	}
	for _, def := range defs {
		if def.Hidden {
			continue
		}
		if value, ok := side[def.Name]; ok {
			out[r.sideColumnKey(def.Name, isSource)] = value
		}
	}
}

// Table builds the side-by-side clicky table.
func (r *ReconcileResult) Table() api.TextTable {
	columns := r.Columns()
	if len(r.Rows) == 0 {
		return emptyTable(columns)
	}
	providers := make([]rowProvider, 0, len(r.Rows))
	for _, row := range r.Flatten() {
		providers = append(providers, rowProvider{cols: columns, row: row})
	}
	return api.NewTableFrom(providers)
}

// Render formats the result in the given clicky format (e.g. "pretty", "csv",
// "json", "html").
func (r *ReconcileResult) Render(format string) (string, error) {
	return clicky.Format(r.Table(), clicky.FormatOptions{Format: format})
}

// Pretty renders the summary line plus the joined table, and is what clicky's
// formatter picks up on the CLI.
func (r *ReconcileResult) Pretty() api.Text {
	text := api.Text{
		Content: fmt.Sprintf("%s -> %s  matched=%d only-source=%d only-dest=%d dup-keys=%d\n",
			r.Source, r.Dest, r.Stats.Matched, r.Stats.OnlySource, r.Stats.OnlyDest, r.Stats.DupKeys),
	}
	if warning := r.boundWarning(); warning != "" {
		text.Children = append(text.Children, api.Text{Content: warning, Style: "text-yellow-500"})
	}
	text.Children = append(text.Children, r.Table())
	return text
}

// boundWarning names the sides whose backend cut the read short. A key can only
// be missing from a side that was read in full, so an incomplete run has to say
// which of its findings are not findings.
//
// A key range needs no such warning: it cuts both sides at the same keys, so a
// one-sided key inside it is missing rather than merely unread.
func (r *ReconcileResult) boundWarning() string {
	var side string
	switch {
	case r.SourceTruncated && r.DestTruncated:
		side = "both sides"
	case r.SourceTruncated:
		side = "the source"
	case r.DestTruncated:
		side = "the destination"
	default:
		return ""
	}
	return fmt.Sprintf(
		"%s stopped short of the whole dataset, so a one-sided key here may simply not have been read — reconcile a key range instead of relying on where the read happened to stop\n",
		side)
}

func renderReconcileKey(row ReconcileRow) any {
	if row.Key == "" {
		return api.Text{Content: "(empty)", Style: "text-gray-500"}
	}
	if row.SourceDupCount <= 1 && row.DestDupCount <= 1 {
		return row.Key
	}
	suffix := ""
	if row.SourceDupCount > 1 {
		suffix = fmt.Sprintf("src %d/%d", row.SourceDupIndex, row.SourceDupCount)
	}
	if row.DestDupCount > 1 {
		if suffix != "" {
			suffix += " "
		}
		suffix += fmt.Sprintf("dest %d/%d", row.DestDupIndex, row.DestDupCount)
	}
	return api.Text{Content: fmt.Sprintf("%s (%s)", row.Key, suffix), Style: "text-amber-500"}
}

func renderReconcileStatus(status ReconcileStatus) any {
	switch status {
	case ReconcileMatched:
		return api.Text{Content: "matched", Style: "text-green-500"}
	case ReconcileOnlySource:
		return api.Text{Content: "only source", Style: "text-yellow-500"}
	case ReconcileOnlyDest:
		return api.Text{Content: "only dest", Style: "text-red-500"}
	default:
		return string(status)
	}
}

func renderReconcileTime(at *time.Time) any {
	if at == nil || at.IsZero() {
		return ""
	}
	return at.Format("15:04:05.000")
}

func renderTimeDiff(diff *time.Duration) any {
	if diff == nil {
		return ""
	}
	text := humanizeDuration(*diff)
	if *diff > timeDiffAlarmThreshold || *diff < -timeDiffAlarmThreshold {
		return api.Text{Content: text, Style: "text-red-500"}
	}
	return text
}

func humanizeDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	sign := ""
	if d < 0 {
		sign, d = "-", -d
	}
	switch {
	case d < time.Microsecond:
		return fmt.Sprintf("%s%dns", sign, d.Nanoseconds())
	case d < time.Millisecond:
		return fmt.Sprintf("%s%dµs", sign, d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%s%dms", sign, d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%s%.2fs", sign, d.Seconds())
	default:
		return sign + d.Truncate(time.Millisecond).String()
	}
}
