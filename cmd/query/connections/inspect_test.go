package connections

import (
	"reflect"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	clickyflags "github.com/flanksource/clicky/flags"
	inspection "github.com/flanksource/commons-db/inspect"
	opensearchinspect "github.com/flanksource/commons-db/inspect/opensearch"
	sqlinspect "github.com/flanksource/commons-db/inspect/sql"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("connection inspection action", func() {
	It("keeps lookup-owned target details out of the generated action form", func() {
		fields, err := clickyflags.ParseStructFields(reflect.TypeOf(InspectFlags{}))
		Expect(err).ToNot(HaveOccurred())
		hidden := map[string]bool{}
		for _, field := range fields {
			hidden[field.FlagName] = field.Hidden
		}
		Expect(hidden).To(HaveKeyWithValue("schema", true))
		Expect(hidden).To(HaveKeyWithValue("target-kind", true))
		Expect(hidden).To(HaveKeyWithValue("database", false))
		Expect(hidden).To(HaveKeyWithValue("target", false))
		Expect(hidden).To(HaveKeyWithValue("sample", false))
	})

	It("returns SQL targets that rerun the entity action in the same dialog", func() {
		targets := connectionInspectionTargets("database-main", InspectFlags{Database: "app", Sample: true, Refresh: true}, browserInspection{
			Kind: "sql",
			Schemas: []sqlinspect.Schema{{Name: "public", Relations: []sqlinspect.Relation{{
				Name: "orders", Type: "table", Columns: []sqlinspect.Column{{Name: "id", DataType: "uuid"}, {Name: "total", DataType: "numeric"}},
			}}}},
		})

		Expect(targets).To(HaveLen(1))
		Expect(targets[0].Name).To(Equal("public.orders"))
		Expect(targets[0].Inspect.Command).To(Equal("connection/inspect"))
		Expect(targets[0].Inspect.Args).To(Equal([]string{"database-main"}))
		Expect(targets[0].Inspect.Target).To(Equal(api.LinkTargetDialog))
		Expect(targets[0].Inspect.AutoRun).To(BeTrue())
		Expect(targets[0].Inspect.Flags).To(Equal(map[string]string{
			"database": "app", "schema": "public", "target": "orders", "sample": "true", "refresh": "true",
		}))
		formatted, err := clicky.Format(&ConnectionInspection{Targets: targets}, clicky.FormatOptions{Format: "clicky-json"})
		Expect(err).ToNot(HaveOccurred())
		Expect(formatted).To(And(
			ContainSubstring(`"kind": "link-command"`),
			ContainSubstring(`"command": "connection/inspect"`),
			ContainSubstring(`"target": "Dialog"`),
		))
	})

	It("projects sampled cardinality onto catalog fields without losing database types", func() {
		connection := &models.Connection{Name: "warehouse", Type: models.ConnectionTypePostgres}
		target := ConnectionInspectionTarget{Name: "public.orders", Target: "orders", Kind: "table", Schema: "public"}
		inspected := browserInspection{Kind: "sql", Schemas: []sqlinspect.Schema{{Name: "public", Relations: []sqlinspect.Relation{{
			Name: "orders", Type: "table", Columns: []sqlinspect.Column{{Name: "status", DataType: "varchar"}},
		}}}}}
		profile, err := connectionInspectionSampleProfile(connectionInspectionSampleRequest{
			Connection: connection,
			Descriptor: browserDescriptor{Provider: "postgres"},
			Options:    InspectFlags{Database: "app", Sample: true},
			Target:     target,
			Inspected:  inspected,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(profile.Query).To(Equal(`SELECT * FROM "public"."orders"`))
		Expect(profile.Provider.Options).To(Equal(map[string]any{"database": "app"}))
		Expect(profile.Columns).To(Equal([]query.ColumnDef{{Name: "status", Type: query.ColumnTypeString}}))

		base, err := connectionInspectionResult(
			connection,
			browserDescriptor{RowLimits: &browserRowLimits{PageSize: 100, MaxPageSize: 1_000, MaxExportRows: 100_000}},
			target,
			inspected,
		)
		Expect(err).ToNot(HaveOccurred())
		sampled := query.NewProfileInspectionResult(profile, &query.SampleResult{
			Columns:       profile.Columns,
			ResultColumns: []query.ResultColumn{{Name: "status"}},
			RenderedQuery: profile.Query,
			Inspection: &query.InspectionStatus{
				Status: "complete", Counts: map[string]int64{"status": 3},
			},
		})

		result := mergeConnectionInspectionResult(base, sampled)
		Expect(result.Fields).To(HaveLen(1))
		Expect(result.Fields[0].DatabaseType).To(Equal("varchar"))
		Expect(result.Fields[0].Cardinality).To(Equal(&query.InspectionCardinality{Value: 3, Relation: "Exact"}))
		Expect(result.Query).To(Equal(profile.Query))
		Expect(result.StatusNote).To(ContainSubstring("cardinality"))
	})

	It("uses qualified SQL targets as lookup values and resolves them back to action flags", func() {
		opts := InspectFlags{
			Target: "public.orders",
			inspected: &browserInspection{Kind: "sql", Schemas: []sqlinspect.Schema{{Name: "public", Relations: []sqlinspect.Relation{{
				Name: "orders", Type: "table",
			}}}}},
		}
		filter := inspectTargetFilter{}
		selected, err := filter.Lookup(&opts)
		Expect(err).ToNot(HaveOccurred())
		Expect(selected).To(HaveKey("public.orders"))
		Expect(opts.Schema).To(Equal("public"))
		Expect(opts.Target).To(Equal("orders"))
		Expect(filter.Options(opts)).To(HaveKey("public.orders"))
	})

	It("keeps connection-only filter and paging facts explicitly unresolved", func() {
		cache := inspection.CacheMetadata{Policy: "opensearch-fields", State: inspection.CacheStateFresh, Cached: true, AgeMS: 250}
		target := ConnectionInspectionTarget{Name: "logs-*", Kind: "pattern"}
		result, err := connectionInspectionResult(
			&models.Connection{Name: "search-main", Type: models.ConnectionTypeOpenSearch},
			browserDescriptor{DefaultQuery: `{"query":{"match_all":{}}}`, RowLimits: &browserRowLimits{PageSize: 100, MaxPageSize: 1_000, MaxExportRows: 100_000}},
			target,
			browserInspection{Kind: "opensearch", Cache: &cache, Selected: &opensearchinspect.FieldCatalog{Fields: []opensearchinspect.Field{{
				Name: "service.name", Types: []string{"keyword"}, Searchable: true, Aggregatable: true,
			}}}},
		)

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Fields).To(Equal([]query.InspectionField{{
			ID: "service.name", Name: "service.name", DatabaseType: "keyword", SemanticType: "string",
			Filter: query.InspectionFilterResolution{
				Label: "Profile required", Kind: "none", Origin: "Unresolved",
				Reason: "Catalog marks this field searchable and aggregatable; a profile query is required to resolve a filter",
			},
		}}))
		Expect(result.Fields[0].Cardinality).To(BeNil())
		Expect(result.Paging).To(Equal(query.InspectionPaging{
			Selected: "Offset", Supported: []string{"Offset"}, Execution: "Browser", Order: "Query-defined", Consistency: "Live",
			Note: "The connection browser exposes bounded offset pages. Stable profile paging resolves from a profile order.",
			Limits: []query.InspectionLimit{
				{Label: "Page size", Value: 100, Origin: "Provider default"},
				{Label: "Max page", Value: 1_000, Origin: "Provider default"},
				{Label: "Export cap", Value: 100_000, Origin: "Provider default"},
			},
		}))
	})
})
