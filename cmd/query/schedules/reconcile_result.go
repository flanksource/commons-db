package schedules

import (
	"github.com/flanksource/commons-db/query"
)

// reconcileResult projects a reconciliation onto the flat rows-and-columns shape
// every report format already understands.
//
// It reuses the same SnapshotColumns/SnapshotRows projection the reconciliation
// snapshot persists, so a scheduled reconciliation report and the rows a
// snapshot serves are the same data rather than two renderings that could
// disagree about what a column is called.
func reconcileResult(reconciled *query.ReconcileResult) (*query.Result, []query.ColumnDef) {
	if reconciled == nil {
		return &query.Result{}, nil
	}
	result := &query.Result{
		Profile:   reconciled.Source,
		Rows:      reconciled.SnapshotRows(),
		Truncated: reconciled.SourceTruncated || reconciled.DestTruncated,
	}
	return result, reconciled.SnapshotColumns()
}
