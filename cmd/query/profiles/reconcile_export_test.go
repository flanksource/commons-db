package profiles

import (
	"context"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/rpc"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
)

var _ = Describe("reconcile export metadata", func() {
	It("advertises the native Clicky formats on the reconcile action only", func() {
		spec := &rpc.OpenAPISpec{Paths: map[string]rpc.OpenAPIPath{
			"/api/v1/profiles/{id}/reconcile": {"get": {
				Clicky: &rpc.ClickyOperationMeta{Surface: "profiles", ActionName: "reconcile"},
			}},
			"/api/v1/profiles/{id}/replay": {"get": {
				Clicky: &rpc.ClickyOperationMeta{Surface: "profiles", ActionName: "replay"},
			}},
		}}

		addReconcileExportMeta(spec)

		reconcile := spec.Paths["/api/v1/profiles/{id}/reconcile"]["get"]
		Expect(reconcile.Clicky.Export).To(Equal(&rpc.ExportMeta{
			Formats: []string{"json", "yaml", "csv", "markdown", "html", "pdf", "excel"},
		}))
		Expect(spec.Paths["/api/v1/profiles/{id}/replay"]["get"].Clicky.Export).To(BeNil())
	})

	It("serves the read-only reconcile action over GET for native Clicky downloads", func() {
		store, err := NewFileStore(GinkgoT().TempDir())
		Expect(err).ToNot(HaveOccurred())
		service, err := New(Options{
			Store:      func() (Store, error) { return store, nil },
			Context:    func() dbcontext.Context { return dbcontext.New() },
			DecodeBody: func(_ context.Context, body map[string]any) (map[string]any, error) { return body, nil },
		})
		Expect(err).ToNot(HaveOccurred())
		service.RegisterClicky()

		root := &cobra.Command{Use: "query"}
		clicky.GenerateCLI(root)
		spec, err := rpc.NewOpenAPIGenerator(nil).GenerateFromCobra(root)
		Expect(err).ToNot(HaveOccurred())

		methods := spec.Paths["/api/v1/profiles/{id}/reconcile"]
		Expect(methods).To(HaveKey("get"))
		Expect(methods).ToNot(HaveKey("post"))
	})

	DescribeTable("selects only the requested reconcile outcome before formatting",
		func(outcome string, expectedKeys []string, expectedStats query.ReconcileStats) {
			result := &query.ReconcileResult{Rows: []query.ReconcileRow{
				{Key: "matched", Status: query.ReconcileMatched},
				{Key: "never-arrived", Status: query.ReconcileOnlySource},
				{Key: "no-counterpart", Status: query.ReconcileOnlyDest},
				{
					Key: "ambiguous", Status: query.ReconcileMatched,
					SourceDupIndex: 1, SourceDupCount: 2, DestDupIndex: 1, DestDupCount: 1,
				},
				{
					Key: "ambiguous", Status: query.ReconcileMatched,
					SourceDupIndex: 2, SourceDupCount: 2, DestDupIndex: 1, DestDupCount: 1,
				},
			}}

			Expect(selectReconcileOutcome(result, outcome)).To(Succeed())
			Expect(result.Rows).To(HaveLen(len(expectedKeys)))
			for index, key := range expectedKeys {
				Expect(result.Rows[index].Key).To(Equal(key))
			}
			Expect(result.Stats).To(Equal(expectedStats))
		},
		Entry("matched", "matched", []string{"matched", "ambiguous", "ambiguous"}, query.ReconcileStats{Matched: 2, DupKeys: 1}),
		Entry("never arrived", "only_source", []string{"never-arrived"}, query.ReconcileStats{OnlySource: 1}),
		Entry("no counterpart", "only_dest", []string{"no-counterpart"}, query.ReconcileStats{OnlyDest: 1}),
		Entry("ambiguous", "ambiguous", []string{"ambiguous", "ambiguous"}, query.ReconcileStats{Matched: 1, DupKeys: 1}),
	)

	It("rejects an outcome the results view does not expose", func() {
		result := &query.ReconcileResult{}
		Expect(selectReconcileOutcome(result, "everything")).To(MatchError(
			`invalid reconcile outcome "everything": expected matched, only_source, only_dest, or ambiguous`,
		))
	})
})
