// Specs for a dynamic result type: its profile gains a column for every key
// its streams bring beyond the type's own.
package recordresults_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/recordstore/recordresults/recordresultstest"
)

var _ = Describe("a dynamic result type", func() {
	It("serves the columns its streams inferred, filtered as the type's own string columns are", func() {
		ctx := context.Background()
		results := recordresultstest.OpenResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: recordresultstest.LocalSettings(recordstore.BackendSQLite),
			Register: func(registry *recordresults.Registry) error {
				return recordresults.RegisterResultType(registry, recordresults.ResultType[recordresultstest.SampleEvent]{
					Kind: "sample_event", Title: "Sample events", Dynamic: true,
				})
			},
		})
		profile, err := results.Registry.Get(ctx, "trace-results/sample_event")
		Expect(err).ToNot(HaveOccurred())
		Expect(profile.Columns).ToNot(ContainElement(HaveField("Name", "region")))

		row, err := recordstore.EncodeRow(recordresultstest.SampleEvents(1, 1)[0])
		Expect(err).ToNot(HaveOccurred())
		row["region"] = "eu"
		_, err = results.Backend.Append(ctx, "run-1", "sample_event", []recordstore.Row{row})
		Expect(err).ToNot(HaveOccurred())

		profile, err = results.Registry.Get(ctx, "trace-results/sample_event")
		Expect(err).ToNot(HaveOccurred())
		Expect(profile.Columns).To(ContainElement(And(
			HaveField("Name", "region"), HaveField("Type", query.ColumnTypeString),
			HaveField("Filter", Equal(&query.ColumnFilterDef{Kind: query.ColumnFilterKindMatch})),
		)))
		listed, err := results.Registry.List(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(listed).To(ContainElement(HaveField("Columns", ContainElement(HaveField("Name", "region")))))
	})

	It("declares its kind dynamic, so its stores add the columns", func() {
		schemas := recordstore.NewSchemas()
		recordresultstest.OpenResults(recordresults.OpenOptions{
			Prefix: "trace-results", ConnectionName: "index", Settings: recordresultstest.LocalSettings(recordstore.BackendSQLite),
			Schemas: schemas,
			Register: func(registry *recordresults.Registry) error {
				return recordresults.RegisterResultType(registry, recordresults.ResultType[recordresultstest.SampleEvent]{
					Kind: "sample_event", Title: "Sample events", Dynamic: true, MaxDynamicColumns: 8,
				})
			},
		})
		schema, err := schemas.Kind("sample_event")
		Expect(err).ToNot(HaveOccurred())
		Expect(schema.Options.Dynamic).To(BeTrue())
		Expect(schema.Options.MaxDynamicColumns).To(Equal(8))
	})
})
