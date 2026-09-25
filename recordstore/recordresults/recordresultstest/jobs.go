// Job events and the views over them: the result types the view specs and the
// view benchmark group, page and follow.
package recordresultstest

import (
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore/recordresults"
)

// JobEvent is a result type whose rows a view groups: each job starts and,
// unless it is still running, ends with a status and a duration.
type JobEvent struct {
	ID     string  `json:"id"`
	Parent string  `json:"parent"`
	Job    string  `json:"job"`
	Phase  string  `json:"phase"`
	Status string  `json:"status"`
	Millis float64 `json:"millis"`
}

// AuditEvent is a second kind, with a view of the same name as one of
// JobEvent's, so a view name is shown to be scoped to its kind.
type AuditEvent struct {
	Job string `json:"job"`
}

// NumberColumn is a view output column holding a number.
func NumberColumn(name string) query.ColumnDef {
	return query.ColumnDef{Name: name, Type: query.ColumnTypeNumber}
}

// JobViews declares overview before the views it uses, so resolving uses
// cannot lean on declaration order.
func JobViews() []recordresults.ResultView {
	return []recordresults.ResultView{
		{
			Name: "overview", Title: "Overview", Uses: []string{"busy_jobs"},
			Params: []query.ParamDef{{Name: "minMs", Label: "Slow from", Type: query.ParamTypeNumber, Required: true}},
			Query: `WITH slow AS (SELECT job FROM busy_jobs WHERE totalMs >= {{.params.minMs}})
SELECT 'all' AS scope, (SELECT count(*) FROM jobs) AS jobs, (SELECT count(*) FROM busy_jobs) AS busy, (SELECT count(*) FROM slow) AS slow`,
			Columns: []query.ColumnDef{{Name: "scope"}, NumberColumn("jobs"), NumberColumn("busy"), NumberColumn("slow")},
			Order:   query.Order{{Column: "scope", Unique: true}},
		},
		{
			Name: "jobs", Title: "Jobs",
			Query: `-- one row per job, whichever of its events the window holds
WITH events AS (SELECT job, phase, millis, seq FROM stream_rows)
SELECT job, count(*) AS events, sum(phase = 'end') AS ended, total(millis) AS totalMs, max(seq) AS lastSeq
FROM events GROUP BY job`,
			Columns: []query.ColumnDef{{Name: "job"}, NumberColumn("events"), NumberColumn("ended"), NumberColumn("totalMs"), NumberColumn("lastSeq")},
			Order:   query.Order{{Column: "totalMs", Desc: true}, {Column: "job", Unique: true}},
		},
		{
			Name: "busy_jobs", Title: "Busy jobs", Uses: []string{"jobs"},
			Query:   `SELECT job, totalMs FROM jobs WHERE ended > 0`,
			Columns: []query.ColumnDef{{Name: "job"}, NumberColumn("totalMs")},
			Order:   query.Order{{Column: "job", Unique: true}},
		},
	}
}

// RegisterJobTypes registers job_event with views, and audit_event with a view
// named like one of JobViews.
func RegisterJobTypes(views []recordresults.ResultView) func(*recordresults.Registry) error {
	return func(registry *recordresults.Registry) error {
		if err := recordresults.RegisterResultType(registry, recordresults.ResultType[JobEvent]{
			Kind: "job_event", Title: "Job events", KeyColumn: "id", Follow: true, SearchColumns: []string{"job"},
			Hierarchy: &recordresults.HierarchyColumns{ID: "id", Parent: "parent"}, Views: views,
		}); err != nil {
			return err
		}
		return recordresults.RegisterResultType(registry, recordresults.ResultType[AuditEvent]{
			Kind: "audit_event", Title: "Audit events",
			Views: []recordresults.ResultView{{
				Name: "jobs", Title: "Audited jobs", Query: `SELECT job, count(*) AS audits FROM stream_rows GROUP BY job`,
				Columns: []query.ColumnDef{{Name: "job"}, NumberColumn("audits")},
				Order:   query.Order{{Column: "job", Unique: true}},
			}},
		})
	}
}
