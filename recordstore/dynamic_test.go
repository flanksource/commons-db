// Specs for inferring a column's type from a value, for kinds that add a
// column for every key their rows bring.
package recordstore_test

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

var _ = Describe("InferColumnType", func() {
	DescribeTable("types a column by the JSON value class of what it holds",
		func(value any, expected query.ColumnType) {
			inferred, ok := recordstore.InferColumnType(value)
			Expect(ok).To(BeTrue())
			Expect(inferred).To(Equal(expected))
		},
		Entry("a string", "a", query.ColumnTypeString),
		Entry("a time, which is its text, never a datetime", time.Now(), query.ColumnTypeString),
		Entry("bytes, which are their text", []byte("a"), query.ColumnTypeString),
		Entry("an int", 3, query.ColumnTypeNumber),
		Entry("an int64", int64(3), query.ColumnTypeNumber),
		Entry("a float", 1.5, query.ColumnTypeNumber),
		Entry("a JSON number", json.Number("7"), query.ColumnTypeNumber),
		Entry("a duration, which is its number", time.Second, query.ColumnTypeNumber),
		Entry("a boolean", true, query.ColumnTypeBoolean),
		Entry("a map", map[string]any{"a": 1}, query.ColumnTypeJSON),
		Entry("a slice", []string{"a"}, query.ColumnTypeJSON),
		Entry("a struct", struct{ A int }{1}, query.ColumnTypeJSON),
		Entry("raw JSON of an object", json.RawMessage(`{"a":1}`), query.ColumnTypeJSON),
		Entry("raw JSON of a string", json.RawMessage(`"a"`), query.ColumnTypeString),
		Entry("raw JSON of a number", json.RawMessage(`-2.5`), query.ColumnTypeNumber),
		Entry("raw JSON of a boolean", json.RawMessage(`false`), query.ColumnTypeBoolean),
	)

	It("types nothing from a null", func() {
		_, ok := recordstore.InferColumnType(nil)
		Expect(ok).To(BeFalse())
		_, ok = recordstore.InferColumnType(json.RawMessage(`null`))
		Expect(ok).To(BeFalse())
		var missing *time.Time
		_, ok = recordstore.InferColumnType(missing)
		Expect(ok).To(BeFalse())
	})

	It("refuses a negative cap on inferred columns and caps them at 256 by default", func() {
		columns := []query.ColumnDef{{Name: "name", Type: query.ColumnTypeString}}
		Expect(recordstore.NewSchemas().Register("dynamic", columns, recordstore.KindOptions{Dynamic: true, MaxDynamicColumns: -1})).
			To(MatchError(ContainSubstring("negative")))
		Expect(recordstore.KindOptions{Dynamic: true}.DynamicColumnLimit()).To(Equal(256))
		Expect(recordstore.KindOptions{Dynamic: true, MaxDynamicColumns: 3}.DynamicColumnLimit()).To(Equal(3))
	})

	It("codes a batch entry refused for a mismatched type", func() {
		err := recordstore.NewBatchError(recordstore.ErrSchemaMismatch)
		Expect(err.Code).To(Equal(recordstore.BatchErrorMismatch))
		Expect(err).To(MatchError(recordstore.ErrSchemaMismatch))
	})
})
