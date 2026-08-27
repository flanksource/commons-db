package query

import (
	"strings"

	"github.com/flanksource/clicky/api"
	inspection "github.com/flanksource/commons-db/inspect"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("inspection result rendering", func() {
	It("keeps source-aware cardinality, filter, paging, icons, and colors without an evidence column", func() {
		profile := Profile{
			Name:     "Orders emitted",
			Provider: ProviderConfig{Type: "opensearch", Connection: "connection://search-main"},
			Columns: []ColumnDef{
				{Name: "service", Source: "service.name", Type: ColumnTypeString},
				{Name: "duration", Type: ColumnTypeDuration, Filter: &ColumnFilterDef{Kind: ColumnFilterKindRange}},
			},
			Limits: &RowLimits{PageSize: 50},
		}
		result := NewProfileInspectionResult(profile, &SampleResult{
			Columns: profile.Columns,
			ResultColumns: []ResultColumn{
				{Name: "service", DatabaseType: "keyword", FilterKey: "filter.service", Filter: &ResultColumnFilter{Kind: "terms", Lookup: true, Multi: true}},
				{Name: "duration", DatabaseType: "long", FilterKey: "filter.duration", Filter: &ResultColumnFilter{Kind: "range"}},
			},
			RenderedQuery: "logs-*",
			DurationMS:    18,
			Pagination:    PageInfo{Mode: "cursor", Consistency: "snapshot"},
			Inspection: &InspectionStatus{
				Status: "complete",
				Counts: map[string]int64{"service.name": DefaultFilterLookupLimit + 1},
				Cache:  []inspection.CacheMetadata{{Policy: "column-cardinality", State: inspection.CacheStateFresh, Cached: true, AgeMS: 1_500}},
			},
			Resolution: SampleResolution{
				PagingModes: "offset,cursor", NativePaging: true, Pageable: true,
				Order:  Order{{Column: "@timestamp", Desc: true}, {Column: "_id", Unique: true}},
				Limits: RowLimits{PageSize: 50, MaxPageSize: 1_000, MaxExportRows: 100_000},
			},
		})

		Expect(result.Fields).To(Equal([]InspectionField{
			{
				ID: "service", Name: "service", Source: "service.name", DatabaseType: "keyword", SemanticType: "string",
				Cardinality: &InspectionCardinality{Value: DefaultFilterLookupLimit + 1, Relation: "At least", Cached: true},
				Filter:      InspectionFilterResolution{Label: "Terms", Kind: "terms", Origin: "Inferred", Field: "service.name", Lookup: true, Multi: true, Reason: "Resolved as terms on filter.service"},
			},
			{
				ID: "duration", Name: "duration", DatabaseType: "long", SemanticType: "duration",
				Filter: InspectionFilterResolution{Label: "Range", Kind: "range", Origin: "Profile override", Field: "duration", Reason: "Resolved as range on filter.duration"},
			},
		}))
		Expect(result.Paging).To(Equal(InspectionPaging{
			Selected: "Cursor", Supported: []string{"Offset", "Cursor"}, Execution: "Native",
			Order: "@timestamp desc, _id unique", Consistency: "Snapshot",
			Note: "This profile has a stable order and can serve positions beyond the first page.",
			Limits: []InspectionLimit{
				{Label: "Page size", Value: 50, Origin: "Profile override"},
				{Label: "Max page", Value: 1_000, Origin: "Provider default"},
				{Label: "Export cap", Value: 100_000, Origin: "Provider default"},
			},
		}))

		columns := result.Fields[0].Columns()
		Expect(columns).To(HaveLen(4))
		Expect([]string{columns[0].Label, columns[1].Label, columns[2].Label, columns[3].Label}).To(Equal(
			[]string{"Field", "Datatype", "Cardinality", "Resolved auto-filter"},
		))
		rendered := result.PrettyFull()
		Expect(rendered.Markdown()).To(ContainSubstring("Fields and resolved filters"))
		Expect(rendered.Markdown()).ToNot(ContainSubstring("Evidence"))
		datatype, ok := result.Fields[0].Row()["datatype"].(api.Text)
		Expect(ok).To(BeTrue())
		Expect(datatype.HTML()).To(ContainSubstring("text-slate-600"))
		Expect(strings.ToLower(rendered.HTML())).To(ContainSubstring("iconify-icon"))
	})
})
