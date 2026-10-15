// Specs for the statements summary: what a capture's statements cost, which
// ran again with other values (an N+1), and the tables and ops they span.

package sqlstatements_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/connection"

	"github.com/flanksource/commons-db/tracing/sqlstatements"
)

var _ = Describe("the statements summary", func() {
	DescribeTable("normalizes a statement to the text its reruns share",
		func(sql, normalized string) {
			Expect(sqlstatements.NormalizeSQL(sql)).To(Equal(normalized))
		},
		Entry("whitespace collapsed and trimmed", "  SELECT *\n\tFROM   todos \n", "SELECT * FROM todos"),
		Entry("string literals, quotes escaped inside", "SELECT * FROM todos WHERE title = 'it''s done' AND id = 'x'",
			"SELECT * FROM todos WHERE title = ? AND id = ?"),
		Entry("numbers, decimals and hex", "UPDATE todos SET rank = 1.5, flags = 0x1F WHERE id = 42",
			"UPDATE todos SET rank = ?, flags = ? WHERE id = ?"),
		Entry("digits inside identifiers and placeholders kept", "SELECT t1.c2 FROM t1 WHERE a = $1 AND b = @p2 AND c = ?",
			"SELECT t1.c2 FROM t1 WHERE a = $1 AND b = @p2 AND c = ?"),
		Entry("quoted identifiers kept", `SELECT "Col 1" FROM "todos 2"`, `SELECT "Col 1" FROM "todos 2"`),
		Entry("comments dropped", "SELECT /* traceparent='00-ab' */ id -- trailing 7\nFROM todos", "SELECT id FROM todos"),
	)

	It("keys statements that differ only by their values and spacing together, and others apart", func() {
		key := sqlstatements.RepeatKey("SELECT * FROM labels WHERE todo_id = 'todo-40'")
		Expect(key).To(MatchRegexp(`^[0-9a-f]{16}$`))
		Expect(sqlstatements.RepeatKey("SELECT  *  FROM labels\nWHERE todo_id = 'todo-41'")).To(Equal(key))
		Expect(sqlstatements.RepeatKey("SELECT * FROM labels WHERE label_id = 'todo-40'")).ToNot(Equal(key))
	})
})

var _ = Describe("summarizing statements", func() {
	const labels = "SELECT l.* FROM labels l JOIN todo_labels tl ON tl.label_id = l.id WHERE tl.todo_id = $1"
	start := time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC)

	statement := func(offset, duration time.Duration, sql string, rows int64, failure string) sqlstatements.StatementRow {
		return sqlstatements.FromStatement(connection.Statement{
			SQL: sql, StartedAt: start.Add(offset), Duration: duration, Rows: rows, Error: failure,
		})
	}

	It("sums what the statements cost, counts the failures, ops, tables and the N+1", func() {
		rows := []sqlstatements.StatementRow{
			statement(0, 4*time.Millisecond, "SELECT * FROM todos WHERE id = 7", 1, ""),
			statement(6*time.Millisecond, 131*time.Millisecond, labels, 2, ""),
			statement(139*time.Millisecond, 128*time.Millisecond, labels, 0, ""),
			statement(269*time.Millisecond, 146*time.Millisecond, labels, 1, ""),
			statement(420*time.Millisecond, 10*time.Millisecond, "UPDATE todo SET done = true WHERE id = 7", -1, `relation "todo" does not exist`),
		}
		Expect(sqlstatements.Summarize(rows)).To(Equal(sqlstatements.Summary{
			Statements: 5, Errors: 1,
			DurationMs: 419, AvgDurationMs: 83.8, P95DurationMs: 146, MaxDurationMs: 146,
			Rows: 4, WindowMs: 430,
			Repeated: 3,
			Repeats: []sqlstatements.Repeat{{
				Key: sqlstatements.RepeatKey(labels), SQL: labels, Count: 3, DurationMs: 405,
			}},
			Tables: []string{"labels", "todo", "todo_labels", "todos"},
			ByOp:   map[string]int{"SELECT": 4, "UPDATE": 1},
		}))
	})

	It("summarizes no statements as nothing spent, with empty tables and ops rather than none", func() {
		Expect(sqlstatements.Summarize(nil)).To(Equal(sqlstatements.Summary{Tables: []string{}, ByOp: map[string]int{}}))
	})

	It("lists the statements that ran most often first, then the costliest", func() {
		var rows []sqlstatements.StatementRow
		for _, run := range []struct {
			sql      string
			times    int
			duration time.Duration
		}{
			{"SELECT * FROM a WHERE id = 1", 2, time.Millisecond},
			{"SELECT * FROM b WHERE id = 1", 3, time.Millisecond},
			{"SELECT * FROM c WHERE id = 1", 2, 5 * time.Millisecond},
			{"SELECT * FROM d WHERE id = 1", 1, time.Second},
		} {
			for range run.times {
				rows = append(rows, statement(0, run.duration, run.sql, 1, ""))
			}
		}
		var order []string
		for _, repeat := range sqlstatements.Summarize(rows).Repeats {
			order = append(order, repeat.SQL)
		}
		Expect(order).To(Equal([]string{
			"SELECT * FROM b WHERE id = 1", "SELECT * FROM c WHERE id = 1", "SELECT * FROM a WHERE id = 1",
		}))
	})
})
