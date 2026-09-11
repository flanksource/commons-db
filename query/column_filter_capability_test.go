package query_test

import (
	"fmt"

	dbconnection "github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type arrayCapabilityLookupProvider struct {
	capabilities     dbconnection.BackendCapabilities
	capabilityChecks int
	lookupCalls      int
}

func (*arrayCapabilityLookupProvider) Type() string { return "opensearch" }

func (*arrayCapabilityLookupProvider) Execute(dbcontext.Context, query.ProviderRequest) ([]query.Row, error) {
	return nil, nil
}

func (p *arrayCapabilityLookupProvider) BackendCapabilities(
	dbcontext.Context,
	query.ProviderRequest,
) (dbconnection.BackendCapabilities, error) {
	p.capabilityChecks++
	return p.capabilities, nil
}

func (p *arrayCapabilityLookupProvider) LookupFilterValues(
	_ dbcontext.Context,
	_ query.ProviderRequest,
	_ query.ColumnFilterBinding,
	_ string,
	_ int,
) ([]query.FilterOption, *query.Total, error) {
	p.lookupCalls++
	return []query.FilterOption{{Value: fmt.Sprintf("value-%d", p.lookupCalls)}}, &query.Total{Value: 1, Exact: true}, nil
}

var _ = Describe("array filter capability preflight", func() {
	It("checks before cache access and keys cached values by live capability metadata", func() {
		provider := &arrayCapabilityLookupProvider{capabilities: arrayCapabilities("logs-a", true)}
		query.RegisterProvider(provider)

		request := query.FilterValueLookupRequest{
			Profile: query.Profile{
				Name:     "array capability cache",
				Provider: query.ProviderConfig{Type: "opensearch"},
				Columns: []query.ColumnDef{{
					Name: "labels", Type: query.ColumnTypeJSON,
					Filter: &query.ColumnFilterDef{Field: "labels", Array: true},
				}},
			},
			Key: "filter.labels", Limit: 20,
		}

		first, _, err := query.LookupFilterValues(dbcontext.New(), request)
		Expect(err).ToNot(HaveOccurred())
		Expect(first).To(Equal([]query.FilterOption{{Value: "value-1"}}))

		provider.capabilities = arrayCapabilities("logs-b", true)
		second, _, err := query.LookupFilterValues(dbcontext.New(), request)
		Expect(err).ToNot(HaveOccurred())
		Expect(second).To(Equal([]query.FilterOption{{Value: "value-2"}}))

		provider.capabilities = arrayCapabilities("logs-b", false)
		_, _, err = query.LookupFilterValues(dbcontext.New(), request)
		Expect(err).To(MatchError(ContainSubstring(`does not support capability "arrayFilters"`)))
		Expect(provider.lookupCalls).To(Equal(2))
		Expect(provider.capabilityChecks).To(Equal(3))
	})

	It("preflights an active array sibling before looking up a scalar field", func() {
		provider := &arrayCapabilityLookupProvider{capabilities: arrayCapabilities("logs", false)}
		query.RegisterProvider(provider)
		request := query.FilterValueLookupRequest{
			Profile: query.Profile{
				Name:     "array sibling lookup",
				Provider: query.ProviderConfig{Type: "opensearch"},
				Columns: []query.ColumnDef{
					{
						Name: "labels", Type: query.ColumnTypeJSON,
						Filter: &query.ColumnFilterDef{Field: "labels", Array: true},
					},
					{Name: "status", Type: query.ColumnTypeString},
				},
			},
			Input: map[string]any{"filter.labels": "customer"},
			Key:   "filter.status", Limit: 20,
		}

		_, _, err := query.LookupFilterValues(dbcontext.New(), request)
		Expect(err).To(MatchError(ContainSubstring(`does not support capability "arrayFilters"`)))
		Expect(provider.lookupCalls).To(BeZero())
		Expect(provider.capabilityChecks).To(Equal(1))
	})

	It("does not preflight an empty array sibling when looking up a scalar field", func() {
		provider := &arrayCapabilityLookupProvider{capabilities: arrayCapabilities("logs", false)}
		query.RegisterProvider(provider)
		request := query.FilterValueLookupRequest{
			Profile: query.Profile{
				Name:     "empty array sibling lookup",
				Provider: query.ProviderConfig{Type: "opensearch"},
				Columns: []query.ColumnDef{
					{
						Name: "labels", Type: query.ColumnTypeJSON,
						Filter: &query.ColumnFilterDef{Field: "labels", Array: true},
					},
					{Name: "status", Type: query.ColumnTypeString},
				},
			},
			Input: map[string]any{"filter.labels": ""},
			Key:   "filter.status", Limit: 20,
		}

		options, _, err := query.LookupFilterValues(dbcontext.New(), request)
		Expect(err).ToNot(HaveOccurred())
		Expect(options).To(Equal([]query.FilterOption{{Value: "value-1"}}))
		Expect(provider.lookupCalls).To(Equal(1))
		Expect(provider.capabilityChecks).To(BeZero())
	})
})

func arrayCapabilities(database string, supported bool) dbconnection.BackendCapabilities {
	return dbconnection.BackendCapabilities{
		Backend: "opensearch", Database: database,
		Features: map[dbconnection.BackendCapability]dbconnection.BackendCapabilityStatus{
			dbconnection.BackendCapabilityArrayFilters: {
				Supported: supported, Detail: "native multi-valued fields",
			},
		},
	}
}
