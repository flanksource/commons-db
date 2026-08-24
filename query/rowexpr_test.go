package query_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
)

var _ = Describe("RowExpr", func() {
	ctx := context.New()

	compile := func(expression string) *query.RowExpr {
		expr, err := query.CompileRowExpr(ctx, expression)
		Expect(err).ToNot(HaveOccurred())
		return expr
	}

	It("evaluates against the caller's bindings rather than flattened row keys", func() {
		expr := compile(`row.severity + ":" + prev.severity`)

		value, err := expr.Eval(map[string]any{
			"row":  map[string]any{"severity": "error"},
			"prev": map[string]any{"severity": "warn"},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(value).To(Equal("error:warn"))
	})

	It("reads a field no row carries as the zero value once the expression is null-safe", func() {
		expr := compile(`(row.message + "").startsWith("at ")`)

		value, err := expr.Bool(map[string]any{"row": map[string]any{"severity": "info"}})

		Expect(err).ToNot(HaveOccurred())
		Expect(value).To(BeFalse())
	})

	It("tells the author how to make a predicate null-safe when it returns null", func() {
		expr := compile(`row.message.startsWith("at ")`)

		_, err := expr.Bool(map[string]any{"row": map[string]any{"severity": "info"}})

		Expect(err).To(MatchError(ContainSubstring("evaluated to null")))
		Expect(err).To(MatchError(ContainSubstring("null-safe")))
	})

	It("reuses one compiled expression across rows with different shapes", func() {
		expr := compile(`row.pod + ""`)

		first, err := expr.Eval(map[string]any{"row": map[string]any{"pod": "billing-api-7f9"}})
		Expect(err).ToNot(HaveOccurred())
		second, err := expr.Eval(map[string]any{"row": map[string]any{"container": "sidecar"}})
		Expect(err).ToNot(HaveOccurred())

		Expect(first).To(Equal("billing-api-7f9"))
		Expect(second).To(Equal(""))
	})

	It("rejects a non-boolean predicate rather than guessing truthiness", func() {
		expr := compile(`row.count`)

		_, err := expr.Bool(map[string]any{"row": map[string]any{"count": 3}})

		Expect(err).To(MatchError(ContainSubstring("expected a boolean")))
	})

	It("converts a CEL-built list of maps into rows", func() {
		expr := compile(`dyn(batch).map(entry, {"message": entry.message, "size": size(batch)})`)

		rows, err := expr.Rows(map[string]any{"batch": []any{
			map[string]any{"message": "one"},
			map[string]any{"message": "two"},
		}})

		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(2))
		Expect(rows[0]["message"]).To(Equal("one"))
		Expect(rows[1]["size"]).To(BeEquivalentTo(2))
	})

	It("rejects a list whose elements are not rows", func() {
		expr := compile(`[1, 2]`)

		_, err := expr.Rows(map[string]any{})

		Expect(err).To(MatchError(ContainSubstring("is not a row")))
	})

	It("names the expression in a compile failure", func() {
		expr := compile(`row.message.noSuchFunction()`)

		_, err := expr.Eval(map[string]any{"row": map[string]any{}})

		Expect(err).To(MatchError(ContainSubstring("noSuchFunction")))
	})

	It("rejects an empty expression at compile time", func() {
		_, err := query.CompileRowExpr(ctx, "")

		Expect(err).To(MatchError(ContainSubstring("expression is empty")))
	})
})
