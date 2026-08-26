package query

import (
	"github.com/flanksource/commons-db/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type columnInspectionProviderStub struct {
	typ       string
	inspected []ColumnDef
}

func (p *columnInspectionProviderStub) Type() string {
	if p.typ != "" {
		return p.typ
	}
	return "column-inspection-test"
}

func (p *columnInspectionProviderStub) Execute(context.Context, ProviderRequest) ([]Row, error) {
	return nil, nil
}

func (p *columnInspectionProviderStub) InspectColumnFilters(
	_ context.Context,
	_ ProviderRequest,
	columns []ColumnDef,
) (ColumnInspectionResult, error) {
	p.inspected = append([]ColumnDef(nil), columns...)
	return ColumnInspectionResult{Filters: map[string]*ColumnFilterDef{
		"message": {Kind: ColumnFilterKindText},
		"status":  {Kind: ColumnFilterKindExact},
	}, Counts: map[string]int64{"message": 42}}, nil
}

var _ = Describe("source-aware column inspection", func() {
	It("inspects supplied catalog columns when the sampled target has no rows", func() {
		original, registered := providerRegistry["postgres"]
		provider := &columnInspectionProviderStub{typ: "postgres"}
		providerRegistry["postgres"] = provider
		DeferCleanup(func() {
			if registered {
				providerRegistry["postgres"] = original
			} else {
				delete(providerRegistry, "postgres")
			}
		})
		profile := Profile{
			Name:     "empty target",
			Provider: ProviderConfig{Type: provider.Type()},
			Query:    "SELECT * FROM empty_target",
			Columns:  []ColumnDef{{Name: "message", Type: ColumnTypeString}},
		}

		result, err := Sample(context.New(), profile, SampleOptions{
			InspectionColumns: profile.Columns,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(provider.inspected).To(Equal(profile.Columns))
		Expect(result.Columns).To(Equal([]ColumnDef{{Name: "message", Type: ColumnTypeString, Filter: &ColumnFilterDef{Kind: ColumnFilterKindText}}}))
		Expect(result.Inspection.Counts).To(Equal(map[string]int64{"message": 42}))
	})

	It("inspects automatic direct columns without replacing explicit author choices", func() {
		provider := &columnInspectionProviderStub{}
		RegisterProvider(provider)
		profile := Profile{
			Provider: ProviderConfig{Type: provider.Type()},
			Columns: []ColumnDef{
				{Name: "status", Type: ColumnTypeString, Filter: &ColumnFilterDef{Kind: ColumnFilterKindTerms}},
				{Name: "computed", Type: ColumnTypeString, CEL: `row.message + "!"`},
			},
		}
		discovered := []ColumnDef{
			{Name: "message", Type: ColumnTypeString},
			{Name: "status", Type: ColumnTypeString},
			{Name: "computed", Type: ColumnTypeString},
			{Name: "retries", Type: ColumnTypeNumber},
		}

		columns, status, err := inspectColumns(context.New(), profile, ProviderRequest{Provider: provider.Type()}, discovered, []ColumnDef{
			{Name: "message", Type: ColumnTypeString},
			{Name: "status", Type: ColumnTypeString},
			{Name: "retries", Type: ColumnTypeNumber},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(status).To(Equal(&InspectionStatus{
			Status: "complete",
			Counts: map[string]int64{"message": 42},
		}))
		Expect(provider.inspected).To(Equal([]ColumnDef{{Name: "message", Type: ColumnTypeString}}))
		Expect(columns).To(Equal([]ColumnDef{
			{Name: "message", Type: ColumnTypeString, Filter: &ColumnFilterDef{Kind: ColumnFilterKindText}},
			{Name: "status", Type: ColumnTypeString},
			{Name: "computed", Type: ColumnTypeString},
			{Name: "retries", Type: ColumnTypeNumber},
		}))
	})
})
