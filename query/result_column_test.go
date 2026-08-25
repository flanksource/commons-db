package query

import (
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

type sampleFilterProvider struct {
	request ProviderRequest
}

func (p *sampleFilterProvider) Type() string { return "postgres" }

func (p *sampleFilterProvider) Execute(_ dbcontext.Context, request ProviderRequest) ([]Row, error) {
	p.request = request
	return []Row{{"message": "started"}}, nil
}

type sampleFilterLookupProvider struct {
	request ProviderRequest
	binding ColumnFilterBinding
}

func (p *sampleFilterLookupProvider) Type() string { return "opensearch" }

func (p *sampleFilterLookupProvider) Execute(_ dbcontext.Context, _ ProviderRequest) ([]Row, error) {
	return nil, nil
}

func (p *sampleFilterLookupProvider) LookupFilterValues(
	_ dbcontext.Context,
	request ProviderRequest,
	binding ColumnFilterBinding,
	_ string,
	_ int,
) ([]FilterOption, *Total, error) {
	p.request, p.binding = request, binding
	return []FilterOption{{Value: "api", Count: 3}}, &Total{Value: 1, Exact: true}, nil
}

var _ = Describe("result column descriptions", func() {
	It("separates resolved browser filters from authoring columns", func() {
		columns, err := DescribeResultColumns(ResultColumnOptions{
			Profile: Profile{
				Provider: ProviderConfig{Type: "opentelemetry"},
				Columns: []ColumnDef{
					{Name: "message", Label: "Log message", Type: ColumnTypeString, Filter: &ColumnFilterDef{Kind: ColumnFilterKindText}},
					{Name: "level", Type: ColumnTypeStatus, Kind: ColumnKindStatus, Filter: &ColumnFilterDef{Options: []string{"info", "error"}}},
					{Name: "trace_id", Type: ColumnTypeUUID},
					{Name: "internal", Hidden: true},
					{Name: "payload", Type: ColumnTypeJSON},
					{Name: "disabled", Filter: &ColumnFilterDef{Disabled: true, Lookup: lo.ToPtr(false)}},
				},
			},
			DatabaseTypes: map[string]string{"message": "text"},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(columns).To(Equal([]ResultColumn{
			{
				Name: "message", Label: "Log message", DatabaseType: "text", FilterKey: "filter.message",
				Filter: &ResultColumnFilter{Kind: string(ColumnFilterKindText)},
			},
			{
				Name: "level", Kind: ColumnKindStatus, FilterKey: "filter.level",
				Filter: &ResultColumnFilter{
					Kind: string(ColumnFilterKindTerms), Multi: true,
					Options: []ResultColumnFilterOption{{Value: "info"}, {Value: "error"}},
				},
			},
			{
				Name: "trace_id", FilterKey: "filter.trace_id",
				Filter: &ResultColumnFilter{Kind: string(ColumnFilterKindExact), Multi: true},
			},
			{Name: "payload"},
			{Name: "disabled"},
		}))
	})

	It("does not advertise filters a provider cannot apply", func() {
		columns, err := DescribeResultColumns(ResultColumnOptions{Profile: Profile{
			Provider: ProviderConfig{Type: "http"},
			Columns:  []ColumnDef{{Name: "message", Filter: &ColumnFilterDef{Kind: ColumnFilterKindText}}},
		}})

		Expect(err).ToNot(HaveOccurred())
		Expect(columns).To(Equal([]ResultColumn{{Name: "message"}}))
	})

	It("returns authoring columns and browser columns as separate sample contracts", func() {
		original := providerRegistry["postgres"]
		providerRegistry["postgres"] = sampleTestProvider{rows: []Row{{
			"message": "started", "occurred": time.Date(2026, 8, 23, 7, 30, 0, 0, time.UTC),
		}}}
		DeferCleanup(func() { providerRegistry["postgres"] = original })

		result, err := Sample(dbcontext.New(), Profile{
			Name: "sample", Query: "SELECT message, occurred FROM logs",
			Provider: ProviderConfig{Type: "postgres"},
		}, SampleOptions{})

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Columns).To(Equal([]ColumnDef{
			{Name: "message", Type: ColumnTypeString},
			{Name: "occurred", Type: ColumnTypeDateTime},
		}))
		Expect(result.ResultColumns).To(Equal([]ResultColumn{
			{Name: "message", FilterKey: "filter.message", Filter: &ResultColumnFilter{
				Kind: string(ColumnFilterKindTerms), Lookup: true, Multi: true,
			}},
			{Name: "occurred", FilterKey: "filter.occurred", Filter: &ResultColumnFilter{
				Kind: string(ColumnFilterKindTime),
			}},
		}))
	})

	It("applies sampled filters through the echoed authoring columns", func() {
		original := providerRegistry["postgres"]
		provider := &sampleFilterProvider{}
		providerRegistry["postgres"] = provider
		DeferCleanup(func() { providerRegistry["postgres"] = original })

		_, err := Sample(dbcontext.New(), Profile{
			Name: "sample", Query: "SELECT message FROM logs",
			Provider: ProviderConfig{Type: "postgres"},
		}, SampleOptions{
			Filters: map[string]string{"filter.message": "started"},
			FilterColumns: []ColumnDef{{
				Name: "message", Type: ColumnTypeString,
			}},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(provider.request.Filters).To(HaveLen(1))
		Expect(provider.request.Filters[0].Key).To(Equal("filter.message"))
		Expect(provider.request.Filters[0].Field).To(Equal("message"))
		Expect(provider.request.Filters[0].Include).To(Equal([]string{"started"}))
	})

	It("rejects a filter selection without echoed columns", func() {
		_, err := Sample(dbcontext.New(), Profile{
			Name: "sample", Query: "SELECT message FROM logs",
			Provider: ProviderConfig{Type: "postgres"},
		}, SampleOptions{Filters: map[string]string{"filter.message": "started"}})

		Expect(err).To(MatchError(ContainSubstring(`column filter "filter.message" is not supported`)))
	})

	It("looks up sampled filter values under the same read-only profile contract", func() {
		original := providerRegistry["opensearch"]
		provider := &sampleFilterLookupProvider{}
		providerRegistry["opensearch"] = provider
		DeferCleanup(func() { providerRegistry["opensearch"] = original })

		options, total, err := SampleFilterValues(dbcontext.New(), Profile{
			Name: "sample", Query: `{ "query": { "match_all": {} } }`,
			Provider: ProviderConfig{Type: "opensearch"},
		}, SampleFilterValuesOptions{
			FilterColumns: []ColumnDef{{Name: "service"}, {Name: "environment"}},
			Filters:       map[string]string{"filter.environment": "prod"},
			FilterKey:     "filter.service",
			Search:        "ap",
			Limit:         20,
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(options).To(Equal([]FilterOption{{Value: "api", Count: 3}}))
		Expect(total).To(Equal(&Total{Value: 1, Exact: true}))
		Expect(provider.binding.Key).To(Equal("filter.service"))
		Expect(provider.request.Filters).To(HaveLen(1))
		Expect(provider.request.Filters[0].Key).To(Equal("filter.environment"))
	})
})
