package query_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

var _ = Describe("EvalExpression", func() {
	ctx := context.New()

	rows := []query.Row{
		{"timestamp": "10:31:02.103", "level": "ERROR", "message": "Timeout after 5006ms"},
		{"timestamp": "10:31:02.104", "level": "ERROR", "message": "\tat com.acme.pay.Gateway.charge(Gateway.java:88)"},
		{"timestamp": "10:30:58.900", "level": "WARN", "message": "Timeout after 31ms calling risk-service"},
	}

	Describe("row scope", func() {
		It("returns one result per row, in order", func() {
			results, err := query.EvalExpression(ctx, "row.level", query.ExpressionOptions{
				Scope: query.ScopeRow,
				Rows:  rows,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(HaveLen(3))
			Expect(results[0].Value).To(Equal("ERROR"))
			Expect(results[2].Value).To(Equal("WARN"))
		})

		It("binds every row key that is a valid identifier as a bare variable", func() {
			results, err := query.EvalExpression(ctx, "level", query.ExpressionOptions{
				Scope: query.ScopeRow,
				Rows:  rows[:1],
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(results[0].Value).To(Equal("ERROR"))
		})

		// The failure mode worth previewing is not an exception. An out-of-range
		// index does not throw here — gomplate's nilsafe library folds it into
		// null — so an expression that is wrong on most of the data still returns
		// cleanly, and only a per-row view shows that it read nothing.
		It("reports a row the expression reads nothing from as null, not as a failure", func() {
			results, err := query.EvalExpression(ctx, `int(row.message.split("after ")[1].split("ms")[0])`,
				query.ExpressionOptions{Scope: query.ScopeRow, Rows: rows})
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(HaveLen(3))
			Expect(results[0].Value).To(BeEquivalentTo(5006))
			Expect(results[1].Error).To(BeEmpty())
			Expect(results[1].Value).To(BeNil())
			Expect(results[1].Type).To(Equal("null"))
			Expect(results[2].Value).To(BeEquivalentTo(31))
		})

		// CEL's null arrives as structpb.NullValue, whose Go value is the integer
		// 0. Passed through untouched it would render as a confident zero — and
		// for a column of durations, 0 is a meaningful reading.
		It("never lets CEL's null reach a caller as the integer zero", func() {
			results, err := query.EvalExpression(ctx, "row.nonexistent", query.ExpressionOptions{
				Scope: query.ScopeRow,
				Rows:  rows[:1],
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(results[0].Value).To(BeNil())
			Expect(results[0].Value).ToNot(BeEquivalentTo(0))
			Expect(results[0].Type).To(Equal("null"))
		})

		It("still reports a genuine compile failure as that row's error", func() {
			results, err := query.EvalExpression(ctx, "nosuchvariable", query.ExpressionOptions{
				Scope: query.ScopeRow,
				Rows:  rows,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(results[0].Error).To(ContainSubstring("undeclared reference"))
		})

		It("names the type of each result, so a column's declared type can be checked", func() {
			results, err := query.EvalExpression(ctx, "size(row.message)", query.ExpressionOptions{
				Scope: query.ScopeRow,
				Rows:  rows[:1],
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(results[0].Type).To(Equal("int"))
		})
	})

	Describe("batch scope", func() {
		It("evaluates once over the whole group, with the batch bindings in scope", func() {
			results, err := query.EvalExpression(ctx, "count", query.ExpressionOptions{
				Scope: query.ScopeBatch,
				Rows:  rows,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(HaveLen(1))
			Expect(results[0].Value).To(BeEquivalentTo(3))
		})

		It("runs the shipped java.stacktrace message expression against a real batch", func() {
			results, err := query.EvalExpression(ctx, `dyn(batch).map(line, line.message + "").join("\n")`,
				query.ExpressionOptions{Scope: query.ScopeBatch, Rows: rows[:2]})
			Expect(err).ToNot(HaveOccurred())
			Expect(results[0].Error).To(BeEmpty())
			Expect(results[0].Value).To(Equal("Timeout after 5006ms\n\tat com.acme.pay.Gateway.charge(Gateway.java:88)"))
		})

		It("keeps the last row when asked, which is what `keep` selects", func() {
			results, err := query.EvalExpression(ctx, "row.level", query.ExpressionOptions{
				Scope: query.ScopeBatch,
				Rows:  rows,
				Keep:  query.KeepLast,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(results[0].Value).To(Equal("WARN"))
		})

		It("reports an un-dyn'd comprehension as the checker's own message", func() {
			results, err := query.EvalExpression(ctx, "batch.map(line, line.message)", query.ExpressionOptions{
				Scope: query.ScopeBatch,
				Rows:  rows,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(results[0].Error).ToNot(BeEmpty())
		})
	})

	Describe("boundary scope", func() {
		// `startsBatch` is only reached once there is a row above, so the first row
		// is never judged. A result for it would preview a decision the engine
		// does not make.
		It("judges every row but the first, naming the row each verdict is about", func() {
			results, err := query.EvalExpression(ctx, "index", query.ExpressionOptions{
				Scope: query.ScopeBoundary,
				Rows:  rows,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(HaveLen(2))
			Expect(results[0].Index).To(Equal(1))
			Expect(results[0].Value).To(BeEquivalentTo(1))
			Expect(results[1].Index).To(Equal(2))
		})

		It("evaluates the shipped continuation predicate to the frame it recognises", func() {
			results, err := query.EvalExpression(ctx,
				`(row.message + "").matches("^\\s*(at\\s|Caused by:|Suppressed:|\\.\\.\\.\\s*[0-9]+\\s+more\\s*$)")`,
				query.ExpressionOptions{Scope: query.ScopeBoundary, Rows: rows})
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(HaveLen(2))
			Expect(results[0].Value).To(BeTrue())  // the `at ...` frame continues the line above
			Expect(results[1].Value).To(BeFalse()) // an ordinary warning does not
		})

		// Binding it always is what the engine does, and an absent binding would
		// not read as null — it fails to compile.
		It("binds prev even where there is no row above, so the predicate compiles", func() {
			results, err := query.EvalExpression(ctx, "prev.message == \"\"", query.ExpressionOptions{
				Scope: query.ScopeBoundary,
				Rows:  rows[:2],
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(results[0].Error).To(BeEmpty())
		})

		It("returns nothing to judge for a single row", func() {
			results, err := query.EvalExpression(ctx, "index", query.ExpressionOptions{
				Scope: query.ScopeBoundary,
				Rows:  rows[:1],
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(BeEmpty())
		})
	})

	Describe("input validation", func() {
		It("refuses an unknown scope rather than guessing one", func() {
			_, err := query.EvalExpression(ctx, "1", query.ExpressionOptions{Scope: "column", Rows: rows})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("column"))
		})

		It("refuses an empty expression", func() {
			_, err := query.EvalExpression(ctx, "  ", query.ExpressionOptions{Scope: query.ScopeRow, Rows: rows})
			Expect(err).To(HaveOccurred())
		})

		It("returns no results for no rows rather than inventing a batch of none", func() {
			results, err := query.EvalExpression(ctx, "count", query.ExpressionOptions{Scope: query.ScopeBatch})
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(BeEmpty())
		})
	})
})
