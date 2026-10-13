// Specs for a statement row: what a capture derives from a statement's text,
// and the columns and styled cells its result profile renders.

package sqlstatements_test

import (
	"time"

	"github.com/flanksource/clicky/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/connection"
	"github.com/flanksource/commons-db/tracing/xetrace"

	"github.com/flanksource/commons-db/tracing/traces/sqlstatements"
)

var _ = Describe("a statement row", func() {
	const labels = "SELECT l.* FROM labels l JOIN todo_labels tl ON tl.label_id = l.id WHERE tl.todo_id = $1"
	startedAt := time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC)

	DescribeTable("derives the statement's op and the tables it names from its text",
		func(sql, op string, tables []string) {
			row := sqlstatements.FromStatement(connection.Statement{SQL: sql})
			Expect(row.Op).To(Equal(op))
			Expect(row.Tables).To(Equal(tables))
		},
		Entry("a join", labels, "SELECT", []string{"labels", "todo_labels"}),
		Entry("an insert", `INSERT INTO "todos" (id, title) VALUES ($1, $2)`, "INSERT", []string{"todos"}),
		Entry("an update", "UPDATE todos SET done = true WHERE id = 7", "UPDATE", []string{"todos"}),
		Entry("a delete", "DELETE FROM public.todo_labels WHERE todo_id = $1", "DELETE", []string{"todo_labels"}),
		Entry("a schema change", "CREATE TABLE notes (id INTEGER PRIMARY KEY)", "DDL", []string{"notes"}),
		Entry("a statement naming no table, whose tables are an empty list", "BEGIN", "OTHER", []string{}),
	)

	It("keys a statement by its normalized text, so reruns with other values share it", func() {
		first := sqlstatements.FromStatement(connection.Statement{SQL: labels, Args: []any{"todo-40"}})
		again := sqlstatements.FromStatement(connection.Statement{SQL: labels, Args: []any{"todo-41"}})
		other := sqlstatements.FromStatement(connection.Statement{SQL: "SELECT * FROM todos WHERE id = $1"})
		Expect(first.RepeatKey).To(Equal(sqlstatements.RepeatKey(labels)))
		Expect(again.RepeatKey).To(Equal(first.RepeatKey))
		Expect(other.RepeatKey).ToNot(Equal(first.RepeatKey))
	})

	It("declares its columns in the order a statements table reads them, hiding the detail", func() {
		type shape struct {
			Label                 string
			Hidden, DefaultHidden bool
		}
		shapes := map[string]shape{}
		var order []string
		for _, column := range (sqlstatements.StatementRow{}).Columns() {
			order = append(order, column.Name)
			shapes[column.Name] = shape{Label: column.Label, Hidden: column.Hidden, DefaultHidden: column.DefaultHidden}
		}
		Expect(order).To(Equal([]string{
			"startedAt", "sql", "op", "tables", "durationMs", "rows",
			"connection", "driver", "error", "args", "repeatKey", "truncated",
		}))
		Expect(shapes).To(Equal(map[string]shape{
			"startedAt": {Label: "Time"}, "sql": {Label: "Statement"}, "op": {Label: "Op"},
			"tables": {Label: "Tables"}, "durationMs": {Label: "Duration"}, "rows": {Label: "Rows"},
			"connection": {Label: "Connection", DefaultHidden: true}, "driver": {Label: "Driver", DefaultHidden: true},
			"error": {Hidden: true}, "args": {Hidden: true}, "repeatKey": {Hidden: true}, "truncated": {Hidden: true},
		}))
	})

	DescribeTable("tones its duration as an xevent's is toned",
		func(duration time.Duration, tone string) {
			Expect(xetrace.DurationStyle(duration)).To(Equal(tone))
			row := sqlstatements.FromStatement(connection.Statement{SQL: labels, Duration: duration})
			cell := row.Row()["durationMs"].(api.TableCell)
			Expect(cell.FilterValue).To(Equal(row.DurationMs))
			Expect(cell.Value).To(HaveField("Style", HavePrefix(tone)))
		},
		Entry("under a millisecond", 400*time.Microsecond, "text-muted"),
		Entry("a few milliseconds", 4*time.Millisecond, "text-yellow-500"),
		Entry("tens of milliseconds", 40*time.Millisecond, "text-orange-500"),
		Entry("hundreds of milliseconds", 400*time.Millisecond, "text-red-500"),
		Entry("seconds", 4*time.Second, "text-red-500 font-bold"),
	)

	It("shows a failed statement's error in red beneath its SQL", func() {
		const failure = `pq: relation "todo_label" does not exist`
		row := sqlstatements.FromStatement(connection.Statement{
			SQL: labels, StartedAt: startedAt, Error: failure, Rows: -1,
		})
		cell := row.Row()["sql"].(api.TableCell)
		Expect(cell.FilterValue).To(Equal(labels))
		statement := cell.Value.(api.Text)
		Expect(statement.String()).To(ContainSubstring(labels))
		Expect(statement.Children).To(ContainElement(And(
			HaveField("Content", failure), HaveField("Style", "text-red-500"),
		)))
	})

	It("shows a statement that did not fail as its SQL alone, and rows it could not count as blank", func() {
		row := sqlstatements.FromStatement(connection.Statement{SQL: labels, StartedAt: startedAt, Rows: -1})
		cells := row.Row()
		Expect(cells["sql"].(api.TableCell).Value).To(Equal(api.CodeBlock("text/x-sql", labels)))
		Expect(cells["rows"].(api.TableCell)).To(Equal(api.TableCell{Value: api.Text{}, FilterValue: int64(-1)}))
	})
})
