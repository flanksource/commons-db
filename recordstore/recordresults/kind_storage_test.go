// Specs for the storage a result type declares beyond its columns: compaction
// rules and indexes, passed to the kind its stores resolve and applied there.
package recordresults_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/recordstore/recordresults/recordresultstest"
)

var _ = Describe("a result type's compaction rules and indexes", func() {
	var rules = []recordstore.CompactRule{{Where: `row.user == "bob"`}}
	var indexes = []recordstore.IndexDef{{Columns: []string{"db"}}}

	open := func() (*recordresults.Results, *recordstore.Schemas) {
		schemas := recordstore.NewSchemas()
		results := recordresultstest.OpenResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: recordresultstest.LocalSettings(recordstore.BackendSQLite),
			Schemas: schemas,
			Register: func(registry *recordresults.Registry) error {
				return recordresults.RegisterResultType(registry, recordresults.ResultType[recordresultstest.SampleEvent]{
					Kind: "sample_event", Title: "Sample events", TimeColumn: "at", Compact: rules, Indexes: indexes,
				})
			},
		})
		return results, schemas
	}

	It("declares them to the kind its stores resolve", func() {
		_, schemas := open()
		schema, err := schemas.Kind("sample_event")
		Expect(err).ToNot(HaveOccurred())
		Expect(schema.Options.Compact).To(Equal(rules))
		Expect(schema.Options.Indexes).To(Equal(indexes))
	})

	It("compacts the rows its rules select from the type's streams", func() {
		results, _ := open()
		ctx := context.Background()
		_, err := recordstore.AppendTyped(ctx, results.Backend, "run-1", "sample_event", recordresultstest.SampleEvents(1, 5))
		Expect(err).ToNot(HaveOccurred())

		compactor, ok := results.Backend.Unwrap().(interface {
			Compact(context.Context) (int, error)
		})
		Expect(ok).To(BeTrue(), "a sqlite results store compacts")
		dropped, err := compactor.Compact(ctx)
		Expect(err).ToNot(HaveOccurred())
		// Events 1, 3 and 5 are bob's; 5 is the stream's last row, which stays.
		Expect(dropped).To(Equal(2))
		meta, err := results.Backend.Meta(ctx, "run-1")
		Expect(err).ToNot(HaveOccurred())
		Expect(meta.Total).To(Equal(int64(3)))
	})
})
