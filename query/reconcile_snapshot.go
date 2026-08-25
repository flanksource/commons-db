package query

import "time"

var reconcileSnapshotColumns = []ColumnDef{
	{Name: "key", Label: "Key"},
	{Name: "status", Label: "Status", Type: ColumnTypeStatus},
	{Name: "outcome", Label: "Outcome", Type: ColumnTypeStatus},
	{Name: "source_time", Label: "Source Time", Type: ColumnTypeDateTime},
	{Name: "dest_time", Label: "Dest Time", Type: ColumnTypeDateTime},
	{Name: "time_diff", Label: "Time Diff", Type: ColumnTypeDuration},
	{Name: "source_dup_index", Label: "Source Duplicate Index", Type: ColumnTypeNumber},
	{Name: "source_dup_count", Label: "Source Duplicate Count", Type: ColumnTypeNumber},
	{Name: "dest_dup_index", Label: "Destination Duplicate Index", Type: ColumnTypeNumber},
	{Name: "dest_dup_count", Label: "Destination Duplicate Count", Type: ColumnTypeNumber},
}

// SnapshotColumns describes the raw reconciliation rows persisted for paging,
// transformations, and exports. Unlike Columns, it contains no api.Text values.
func (r *ReconcileResult) SnapshotColumns() []ColumnDef {
	columns := append([]ColumnDef(nil), reconcileSnapshotColumns...)
	columns = append(columns, r.snapshotSideColumns(true)...)
	columns = append(columns, r.snapshotSideColumns(false)...)
	columns = append(columns, ColumnDef{Name: "row_id", Type: ColumnTypeNumber, Hidden: true})
	return columns
}

func (r *ReconcileResult) snapshotSideColumns(source bool) []ColumnDef {
	profile := r.DestProfile
	if source {
		profile = r.SourceProfile
	}
	definitions := profile.Columns
	if len(definitions) == 0 {
		definitions = deriveColumns(r.sideRows(source))
	}
	columns := make([]ColumnDef, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Hidden {
			continue
		}
		definition.Name = r.sideColumnKey(definition.Name, source)
		if definition.Label == "" {
			definition.Label = definition.Name
		}
		columns = append(columns, definition)
	}
	return columns
}

// SnapshotRows is the unstyled, typed projection stored in SQLite.
func (r *ReconcileResult) SnapshotRows() []Row {
	rows := make([]Row, 0, len(r.Rows))
	for index, row := range r.Rows {
		outcome := string(row.Status)
		if row.SourceDupCount > 1 || row.DestDupCount > 1 {
			outcome = "ambiguous"
		}
		item := Row{
			"row_id": index + 1, "key": row.Key, "status": string(row.Status), "outcome": outcome,
			"source_time": rawReconcileTime(row.SourceTime), "dest_time": rawReconcileTime(row.DestTime),
			"time_diff":        rawReconcileDuration(row.TimeDiff),
			"source_dup_index": row.SourceDupIndex, "source_dup_count": row.SourceDupCount,
			"dest_dup_index": row.DestDupIndex, "dest_dup_count": row.DestDupCount,
		}
		r.copySnapshotSide(item, row.Source, true)
		r.copySnapshotSide(item, row.Dest, false)
		rows = append(rows, item)
	}
	return rows
}

func (r *ReconcileResult) copySnapshotSide(target Row, side Row, source bool) {
	for _, column := range r.snapshotSideColumns(source) {
		target[column.Name] = nil
	}
	if side == nil {
		return
	}
	profile := r.DestProfile
	if source {
		profile = r.SourceProfile
	}
	definitions := profile.Columns
	if len(definitions) == 0 {
		definitions = deriveColumns(r.sideRows(source))
	}
	for _, definition := range definitions {
		if definition.Hidden {
			continue
		}
		if value, found := side[definition.Name]; found {
			target[r.sideColumnKey(definition.Name, source)] = value
		}
	}
}

func rawReconcileTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func rawReconcileDuration(value *time.Duration) any {
	if value == nil {
		return nil
	}
	return int64(*value)
}
