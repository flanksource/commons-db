package query

import (
	"github.com/flanksource/commons-db/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type columnInspectionProviderStub struct {
	inspected []ColumnDef
}

func (p *columnInspectionProviderStub) Type() string { return "column-inspection-test" }

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
	}}, nil
}

var _ = Describe("source-aware column inspection", func() {
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
		Expect(status).To(Equal(&InspectionStatus{Status: "complete"}))
		Expect(provider.inspected).To(Equal([]ColumnDef{{Name: "message", Type: ColumnTypeString}}))
		Expect(columns).To(Equal([]ColumnDef{
			{Name: "message", Type: ColumnTypeString, Filter: &ColumnFilterDef{Kind: ColumnFilterKindText}},
			{Name: "status", Type: ColumnTypeString},
			{Name: "computed", Type: ColumnTypeString},
			{Name: "retries", Type: ColumnTypeNumber},
		}))
	})
})
