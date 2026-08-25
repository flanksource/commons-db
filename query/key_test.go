package query_test

import (
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("KeySpec", func() {
	resolve := func(spec query.KeySpec, row query.Row) (string, error) {
		keyOf, err := spec.Resolver(reconcileCtx())
		if err != nil {
			return "", err
		}
		return keyOf(row)
	}

	It("joins multiple columns in the declared order", func() {
		key, err := resolve(query.KeySpec{Columns: []string{"region", "id"}}, query.Row{"id": 7, "region": "eu"})
		Expect(err).ToNot(HaveOccurred())
		Expect(key).To(Equal("eu\x00" + "7"))
	})

	It("keeps composite keys distinct from a value containing the separator", func() {
		split, err := resolve(query.KeySpec{Columns: []string{"a", "b"}}, query.Row{"a": "x", "b": "y"})
		Expect(err).ToNot(HaveOccurred())
		joined, err := resolve(query.KeySpec{Columns: []string{"a"}}, query.Row{"a": "xy"})
		Expect(err).ToNot(HaveOccurred())
		Expect(split).ToNot(Equal(joined))
	})

	It("reads a value the column key cannot reach", func() {
		key, err := resolve(query.KeySpec{CEL: `row.meta.correlation.id`},
			query.Row{"meta": map[string]any{"correlation": map[string]any{"id": "abc"}}})
		Expect(err).ToNot(HaveOccurred())
		Expect(key).To(Equal("abc"))
	})

	It("binds top-level row keys as CEL identifiers", func() {
		key, err := resolve(query.KeySpec{CEL: `tenant + ":" + string(seq)`}, query.Row{"tenant": "za", "seq": 3})
		Expect(err).ToNot(HaveOccurred())
		Expect(key).To(Equal("za:3"))
	})

	DescribeTable("collapses every spelling of absent onto the empty key",
		func(value any) {
			Expect(query.NormalizeKeyValue(value)).To(BeEmpty())
		},
		Entry("a nil value", nil),
		Entry("the string null", "null"),
		Entry("a formatted nil", "<nil>"),
		Entry("a CEL null", "NULL_VALUE"),
	)

	It("keeps a present value verbatim", func() {
		Expect(query.NormalizeKeyValue(0)).To(Equal("0"))
		Expect(query.NormalizeKeyValue(false)).To(Equal("false"))
		Expect(query.NormalizeKeyValue("")).To(BeEmpty())
	})

	DescribeTable("rejects an unusable spec",
		func(spec query.KeySpec, expected string) {
			Expect(spec.Validate()).To(MatchError(ContainSubstring(expected)))
		},
		Entry("neither source", query.KeySpec{}, "columns or a cel expression"),
		Entry("both sources", query.KeySpec{Columns: []string{"id"}, CEL: `row.id`}, "pick one"),
		Entry("a blank expression", query.KeySpec{CEL: "   "}, "columns or a cel expression"),
	)

	It("accepts exactly one source", func() {
		Expect(query.KeySpec{Columns: []string{"id"}}.Validate()).To(Succeed())
		Expect(query.KeySpec{CEL: `row.id`}.Validate()).To(Succeed())
	})
})
