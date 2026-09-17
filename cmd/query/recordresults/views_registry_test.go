package recordresults_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/cmd/query/recordresults"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// countView is a valid view whose name, uses and query each entry replaces.
func countView(name, statement string, uses ...string) recordresults.ResultView {
	return recordresults.ResultView{
		Name: name, Title: name, Uses: uses, Query: statement,
		Columns: []query.ColumnDef{{Name: "job"}, numberColumn("events")},
		Order:   query.Order{{Column: "job", Unique: true}},
	}
}

const countQuery = `SELECT job, count(*) AS events FROM stream_rows GROUP BY job`

var _ = Describe("declaring views on a result type", func() {
	DescribeTable("refuses a view it could not serve, registering nothing of the type",
		func(views []recordresults.ResultView, message string) {
			registry, _ := newRegistry()
			err := recordresults.RegisterResultType(registry, recordresults.ResultType[jobEvent]{
				Kind: "job_event", Title: "Job events", Views: views,
			})
			Expect(err).To(MatchError(ContainSubstring(message)))
			Expect(registry.ResultTypes()).To(BeEmpty())
			Expect(registry.IsVirtual("trace-results/job_event")).To(BeFalse())
		},
		Entry("a name that is not a SQL identifier",
			[]recordresults.ResultView{countView("job-counts", countQuery)}, `"job-counts" is not a SQL identifier`),
		Entry("a name SQLite only accepts quoted",
			[]recordresults.ResultView{countView("group", countQuery)}, `"group"`),
		Entry("a name used twice, ignoring case",
			[]recordresults.ResultView{countView("counts", countQuery), countView("Counts", countQuery)}, `"Counts" is declared twice`),
		Entry("the stream window's own name",
			[]recordresults.ResultView{countView("stream_rows", countQuery)}, `"stream_rows" is reserved`),
		Entry("the paging wrapper's name",
			[]recordresults.ResultView{countView("__cdb_base", countQuery)}, `"__cdb_base" is reserved`),
		Entry("an unknown used view",
			[]recordresults.ResultView{countView("counts", countQuery, "missing")}, `uses "missing", which is not a view of`),
		Entry("views that use each other",
			[]recordresults.ResultView{countView("first", countQuery, "second"), countView("second", countQuery, "third"), countView("third", countQuery, "first")},
			"first -> second -> third -> first"),
		Entry("a view that uses itself",
			[]recordresults.ResultView{countView("counts", countQuery, "counts")}, "counts -> counts"),
		Entry("a CTE of its own named after the stream window",
			[]recordresults.ResultView{countView("counts", "WITH stream_rows AS (SELECT 'x' AS job) "+countQuery)}, "stream_rows"),
		Entry("a CTE of its own named after the paging wrapper",
			[]recordresults.ResultView{countView("counts", "WITH __cdb_base AS (SELECT job FROM stream_rows) SELECT job, count(*) AS events FROM __cdb_base GROUP BY job")}, "__cdb_base"),
		Entry("a column the kind does not have",
			[]recordresults.ResultView{countView("counts", `SELECT jobb AS job, count(*) AS events FROM stream_rows GROUP BY jobb`)}, "no such column: jobb"),
		Entry("an order column the view does not declare",
			[]recordresults.ResultView{func() recordresults.ResultView {
				view := countView("counts", `SELECT job AS name, count(*) AS events FROM stream_rows GROUP BY job`)
				view.Columns = []query.ColumnDef{{Name: "name"}, numberColumn("events")}
				view.Order = query.Order{{Column: "name"}, {Column: "job", Unique: true}}
				return view
			}()}, `orders by "job", which is not one of its declared columns`),
		Entry("an order that does not end in a unique column",
			[]recordresults.ResultView{func() recordresults.ResultView {
				view := countView("counts", countQuery)
				view.Order = query.Order{{Column: "job"}}
				return view
			}()}, "not declared unique"),
		Entry("a param the stream window already declares",
			[]recordresults.ResultView{func() recordresults.ResultView {
				view := countView("counts", countQuery)
				view.Params = []query.ParamDef{{Name: "toSeq", Type: query.ParamTypeNumber}}
				return view
			}()}, `"toSeq" twice`),
		Entry("an identifier param",
			[]recordresults.ResultView{func() recordresults.ResultView {
				view := countView("counts", countQuery)
				view.Params = []query.ParamDef{{Name: "by", Type: query.ParamTypeIdentifier, Required: true}}
				return view
			}()}, `param "by" is an identifier`),
		Entry("no title",
			[]recordresults.ResultView{func() recordresults.ResultView {
				view := countView("counts", countQuery)
				view.Title = ""
				return view
			}()}, "needs a title"),
	)

	It("refuses a view named after a param the base profile declares", func() {
		registry, _ := newRegistry()
		view := countView("counts", countQuery)
		view.Params = []query.ParamDef{{Name: "rootsOnly", Type: query.ParamTypeBoolean, Default: false}}
		err := recordresults.RegisterResultType(registry, recordresults.ResultType[jobEvent]{
			Kind: "job_event", Title: "Job events", KeyColumn: "id",
			Hierarchy: &recordresults.HierarchyColumns{ID: "id", Parent: "parent"},
			Views:     []recordresults.ResultView{view},
		})
		Expect(err).To(MatchError(ContainSubstring(`param "rootsOnly" belongs to the base profile`)))
	})

	It("fails to open a result store whose view reads a column the kind does not have", func() {
		_, err := recordresults.Open(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: localSettings(recordstore.BackendSQLite),
			Register: registerJobTypes([]recordresults.ResultView{countView("counts", `SELECT job, count(duration) AS events FROM stream_rows GROUP BY job`)}),
		})
		Expect(err).To(MatchError(ContainSubstring("no such column: duration")))
	})
})
