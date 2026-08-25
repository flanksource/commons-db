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

var _ = Describe("reconcile snapshot actions", func() {
	It("keeps source and destination filter overrides independent", func() {
		source := query.Profile{
			Name: "orders-emitted",
			Reconcile: &query.ReconcileConfig{
				Dest:          "orders-ingested",
				SourceFilters: map[string]string{"region": "eu", "tier": "gold"},
				DestFilters:   map[string]string{"region": "us", "tenant": "acme"},
			},
		}

		config, err := reconcileConfig(
			source,
			ReconcileFlags{},
			map[string]string{"region": "za"},
			map[string]string{"tenant": "tenant-x"},
		)

		Expect(err).ToNot(HaveOccurred())
		Expect(config.SourceFilters).To(Equal(map[string]string{"region": "za", "tier": "gold"}))
		Expect(config.DestFilters).To(Equal(map[string]string{"region": "us", "tenant": "tenant-x"}))
	})

	It("decodes CSV action values without splitting a filter's comma-separated selection", func() {
		flags, err := decodeActionFlags[ReconcileFlags](map[string]string{
			"source-filter": `"filter.tenant=acme,blue",region=eu`,
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(flags.SourceFilters).To(Equal([]string{"filter.tenant=acme,blue", "region=eu"}))
	})

	It("accepts normal profile filters but rejects transport and wrong-side filters", func() {
		profile := query.Profile{
			Name:     "orders-emitted",
			Provider: query.ProviderConfig{Type: "postgres"},
			Params: []query.ParamDef{
				{Name: "region"},
				{Name: "from", Role: query.ParamRoleTimeFrom},
				{Name: "limit", Role: query.ParamRoleLimit},
			},
			Columns: []query.ColumnDef{{Name: "tenant"}},
		}

		Expect(validateReconcileFilters(profile, map[string]string{
			"region": "eu", "from": "now-1h", "filter.tenant": "acme",
		}, "source")).To(Succeed())
		Expect(validateReconcileFilters(profile, map[string]string{"limit": "100"}, "source")).To(
			MatchError(`source filter "limit" is not supported by profile "orders-emitted"`))
		Expect(validateReconcileFilters(profile, map[string]string{"service": "api"}, "destination")).To(
			MatchError(`destination filter "service" is not supported by profile "orders-emitted"`))
	})

	It("creates and materializes snapshots over POST instead of exporting the action response", func() {
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

		reconcile := spec.Paths["/api/v1/profiles/{id}/reconcile"]
		Expect(reconcile).To(HaveKey("post"))
		Expect(reconcile["post"].Clicky.Export).To(BeNil())
		materialize := spec.Paths["/api/v1/profiles/{id}/reconcile-materialize"]
		Expect(materialize).To(HaveKey("post"))

		// Reading a snapshot is the one action registered as a GET, so that the
		// results URL is a link rather than a form submission. Actions default to
		// POST, and nothing else in this repo overrides that — this is the whole
		// regression net for it.
		read := spec.Paths["/api/v1/profiles/{id}/reconcile-snapshot"]
		Expect(read).To(HaveKey("get"))
		Expect(read).ToNot(HaveKey("post"))
		var snapshotParam *rpc.OpenAPIParameter
		for _, parameter := range read["get"].Parameters {
			if parameter.Name == "snapshot" {
				snapshotParam = &parameter
			}
		}
		Expect(snapshotParam).ToNot(BeNil(), "the snapshot id must be a parameter")
		// A GET carries its flags in the query string; a body would never be read.
		Expect(snapshotParam.In).To(Equal("query"))
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
