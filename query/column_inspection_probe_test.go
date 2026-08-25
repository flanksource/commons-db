package query

import (
	"github.com/flanksource/commons-db/context"
	inspection "github.com/flanksource/commons-db/inspect"
	"github.com/flanksource/commons/logger"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// cardinalityProviderStub answers with counts as well as suggestions, which is
// what makes the filter kind it chose explainable rather than merely reported.
type cardinalityProviderStub struct {
	counts map[string]int64
	cached bool
}

func (p *cardinalityProviderStub) Type() string { return "cardinality-probe-test" }

func (p *cardinalityProviderStub) Execute(context.Context, ProviderRequest) ([]Row, error) {
	return nil, nil
}

func (p *cardinalityProviderStub) InspectColumnFilters(
	_ context.Context,
	_ ProviderRequest,
	columns []ColumnDef,
) (ColumnInspectionResult, error) {
	filters := map[string]*ColumnFilterDef{}
	for _, column := range columns {
		if p.counts[column.InspectedField()] > DefaultFilterLookupLimit {
			filters[column.Name] = &ColumnFilterDef{Kind: ColumnFilterKindText}
		}
	}
	return ColumnInspectionResult{
		Filters: filters,
		Counts:  p.counts,
		Cache:   []inspection.CacheMetadata{{Cached: p.cached}},
	}, nil
}

var _ = Describe("cardinality probes", func() {
	const connection = "connection://opensearch/logs"

	armedRun := func(stub *cardinalityProviderStub) *Recorder {
		RegisterProvider(stub)
		recorder := NewRecorder(RecorderOptions{ID: "probe-record", Level: logger.Trace})
		profile := Profile{
			Provider: ProviderConfig{Type: stub.Type()},
			Columns: []ColumnDef{
				{Name: "message", Type: ColumnTypeString},
				{Name: "pod", Type: ColumnTypeString, Filter: &ColumnFilterDef{Field: "kubernetes.pod_name"}},
			},
		}
		discovered := []ColumnDef{
			{Name: "message", Type: ColumnTypeString},
			{Name: "pod", Type: ColumnTypeString},
		}
		raw := []ColumnDef{
			{Name: "message", Type: ColumnTypeString},
			{Name: "kubernetes.pod_name", Type: ColumnTypeString},
		}
		_, _, err := inspectColumns(
			WithRecorder(context.New(), recorder),
			profile,
			ProviderRequest{Provider: stub.Type(), Connection: connection},
			discovered, raw,
		)
		Expect(err).ToNot(HaveOccurred())
		return recorder
	}

	It("records the count, the limit it was compared against and the kind it chose", func() {
		recorder := armedRun(&cardinalityProviderStub{counts: map[string]int64{
			"message":             4212,
			"kubernetes.pod_name": 7,
		}})

		Expect(recorder.Detail().Probes).To(ConsistOf(
			CardinalityProbe{
				Provider: "cardinality-probe-test", Connection: connection,
				Column: "message", Distinct: 4212, Limit: DefaultFilterLookupLimit,
				Kind: string(ColumnFilterKindText),
			},
			// Under the limit, so nothing had to change — recorded all the same,
			// because "it was probed and left alone" is the answer to why this
			// column is a dropdown.
			CardinalityProbe{
				Provider: "cardinality-probe-test", Connection: connection,
				Column: "pod", Field: "kubernetes.pod_name", Distinct: 7,
				Limit: DefaultFilterLookupLimit,
			},
		))
		Expect(recorder.Summary().Counts.Probes).To(Equal(2))
	})

	It("says the answer came from cache, so a fast page is not read as a cheap one", func() {
		recorder := armedRun(&cardinalityProviderStub{
			counts: map[string]int64{"message": 1, "kubernetes.pod_name": 1},
			cached: true,
		})

		probes := recorder.Detail().Probes
		Expect(probes).To(HaveLen(2))
		for _, probe := range probes {
			Expect(probe.Cached).To(BeTrue())
		}
	})

	It("costs an unarmed run nothing", func() {
		stub := &cardinalityProviderStub{counts: map[string]int64{"message": 1}}
		RegisterProvider(stub)

		_, _, err := inspectColumns(
			context.New(),
			Profile{Provider: ProviderConfig{Type: stub.Type()}},
			ProviderRequest{Provider: stub.Type()},
			[]ColumnDef{{Name: "message", Type: ColumnTypeString}},
			[]ColumnDef{{Name: "message", Type: ColumnTypeString}},
		)
		Expect(err).ToNot(HaveOccurred())
	})
})
