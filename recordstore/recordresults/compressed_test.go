// Specs for a result type's compressed columns: declared to its stores, and
// refused as search columns, which SQL has to read inside.
package recordresults_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/recordstore/recordresults/recordresultstest"
)

var _ = Describe("a result type's compressed columns", func() {
	register := func(resultType recordresults.ResultType[recordresultstest.SampleEvent]) (*recordstore.Schemas, error) {
		schemas := recordstore.NewSchemas()
		var err error
		recordresultstest.OpenResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: recordresultstest.LocalSettings(recordstore.BackendSQLite),
			Schemas: schemas,
			Register: func(registry *recordresults.Registry) error {
				err = recordresults.RegisterResultType(registry, resultType)
				return nil
			},
		})
		return schemas, err
	}

	It("declares them to the kind its stores resolve", func() {
		schemas, err := register(recordresults.ResultType[recordresultstest.SampleEvent]{Kind: "sample_event", Title: "Sample events", Compressed: []string{"detail"}})
		Expect(err).ToNot(HaveOccurred())
		schema, err := schemas.Kind("sample_event")
		Expect(err).ToNot(HaveOccurred())
		Expect(schema.Options.Compressed).To(Equal([]string{"detail"}))
	})

	It("refuses a compressed column as a search column", func() {
		_, err := register(recordresults.ResultType[recordresultstest.SampleEvent]{
			Kind: "sample_event", Title: "Sample events", Compressed: []string{"detail"}, SearchColumns: []string{"detail"},
		})
		Expect(err).To(MatchError(ContainSubstring(`search column "detail" is compressed`)))
	})
})
