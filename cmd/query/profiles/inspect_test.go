package profiles

import (
	"context"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("profile inspection action", func() {
	It("loads and samples the stored profile instead of accepting a replacement document", func() {
		original, err := query.GetProvider("opensearch")
		Expect(err).ToNot(HaveOccurred())
		query.RegisterProvider(sampleInspectionProvider{})
		DeferCleanup(func() { query.RegisterProvider(original) })

		store, err := NewFileStore(GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		profile := query.Profile{
			Name: "Stored logs", Provider: query.ProviderConfig{Type: "opensearch"}, Query: "{}",
			Columns: []query.ColumnDef{{Name: "service", Type: query.ColumnTypeString}},
		}
		Expect(store.Save(context.Background(), profile)).To(Succeed())
		service, err := New(Options{
			Store:      func() (Store, error) { return store, nil },
			Context:    func() dbcontext.Context { return dbcontext.New() },
			DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
		})
		Expect(err).ToNot(HaveOccurred())

		result, err := service.Inspect(context.Background(), profile.Name, InspectFlags{Refresh: true})

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Name).To(Equal(profile.Name))
		Expect(result.Fields).To(HaveLen(1))
		Expect(result.Fields[0].Cardinality).To(Equal(&query.InspectionCardinality{Value: 3, Relation: "Exact", Cached: false}))
		Expect(result.Fields[0].Filter.Kind).To(Equal("terms"))
	})
})
