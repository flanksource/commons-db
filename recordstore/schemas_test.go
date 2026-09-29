package recordstore_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

var _ = Describe("Schemas", func() {
	columns := []query.ColumnDef{
		{Name: "id", Type: query.ColumnTypeString},
		{Name: "count", Type: query.ColumnTypeNumber},
		{Name: "at", Type: query.ColumnTypeDateTime},
		{Name: "detail", Type: query.ColumnTypeJSON},
	}

	It("resolves a kind to the columns and options it was declared with", func() {
		schemas := recordstore.NewSchemas()
		options := recordstore.KindOptions{Key: "id", Retention: recordstore.RetainRows}
		Expect(schemas.Register("event", columns, options)).To(Succeed())

		Expect(schemas.Kind("event")).To(Equal(recordstore.KindSchema{Kind: "event", Columns: columns, Options: options}))
	})

	It("accepts the same declaration twice and refuses a different one", func() {
		schemas := recordstore.NewSchemas()
		Expect(schemas.Register("event", columns, recordstore.KindOptions{Key: "id"})).To(Succeed())
		Expect(schemas.Register("event", columns, recordstore.KindOptions{Key: "id"})).To(Succeed())

		Expect(schemas.Register("event", columns, recordstore.KindOptions{})).To(MatchError(ContainSubstring("already declared")))
	})

	DescribeTable("refuses a declaration no backend could store",
		func(kind string, options recordstore.KindOptions, message string) {
			Expect(recordstore.NewSchemas().Register(kind, columns, options)).To(MatchError(ContainSubstring(message)))
		},
		Entry("a key that is not a column", "event", recordstore.KindOptions{Key: "missing"}, `key "missing" is not one of its columns`),
		Entry("a key that is not a string column", "event", recordstore.KindOptions{Key: "count"}, "not a string"),
		Entry("an unknown retention", "event", recordstore.KindOptions{Retention: recordstore.Retention(7)}, "retention(7)"),
		Entry("an unknown conflict policy", "event", recordstore.KindOptions{Key: "id", OnConflict: recordstore.OnConflict(7)}, "on conflict(7)"),
		Entry("replacing stored rows with no key to find them by", "event", recordstore.KindOptions{OnConflict: recordstore.OnConflictReplace}, "replaces stored rows, which needs a key"),
		Entry("an invalid kind", "bad kind", recordstore.KindOptions{}, "kind"),
		Entry("a time column that is not a column", "event", recordstore.KindOptions{TimeColumn: "missing"}, `time column "missing" is not one of its columns`),
		Entry("a time column that is not a datetime", "event", recordstore.KindOptions{TimeColumn: "count"}, "not a datetime"),
		Entry("an index of no columns", "event", recordstore.KindOptions{Indexes: []recordstore.IndexDef{{}}}, "index 0 names no columns"),
		Entry("an index of a column it does not declare", "event", recordstore.KindOptions{Indexes: []recordstore.IndexDef{{Columns: []string{"missing"}}}}, `index 0 column "missing" is not one of its columns`),
		Entry("an index naming a column twice", "event", recordstore.KindOptions{Indexes: []recordstore.IndexDef{{Columns: []string{"id", "id"}}}}, `index 0 names column "id" twice`),
		Entry("an index of a structured column", "event", recordstore.KindOptions{Indexes: []recordstore.IndexDef{{Columns: []string{"detail"}}}}, `index 0 column "detail" is a json column`),
	)

	It("accepts a time column and indexes of the kind's columns", func() {
		options := recordstore.KindOptions{TimeColumn: "at", Indexes: []recordstore.IndexDef{{Columns: []string{"id", "count"}}}}
		Expect(recordstore.NewSchemas().Register("event", columns, options)).To(Succeed())
	})

	It("refuses an unknown kind", func() {
		_, err := recordstore.NewSchemas().Kind("event")
		Expect(err).To(MatchError(ContainSubstring(`kind "event" has no declared schema`)))
	})

	It("refuses a resolver that answers a kind with another kind's schema", func() {
		resolver := func(string) (recordstore.KindSchema, error) {
			return recordstore.KindSchema{Kind: "other", Columns: columns}, nil
		}
		_, err := recordstore.ResolveKind(resolver, "event")
		Expect(err).To(MatchError(ContainSubstring(`resolved to the schema of kind "other"`)))
	})
})
