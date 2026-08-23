package query_test

import (
	"fmt"

	querycontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type cachedLookupProvider struct {
	typ   string
	calls int
}

func (p *cachedLookupProvider) Type() string { return p.typ }

func (p *cachedLookupProvider) Execute(querycontext.Context, query.ProviderRequest) ([]query.Row, error) {
	return nil, nil
}

func (p *cachedLookupProvider) LookupFilterValues(
	_ querycontext.Context,
	request query.ProviderRequest,
	_ query.ColumnFilterBinding,
	_ string,
	_ int,
) ([]query.FilterOption, *query.Total, error) {
	p.calls++
	request.Diagnostics.RecordRequest("SELECT status FROM lookup_source", nil, map[string]any{"operation": "filter-values"})
	return []query.FilterOption{{Value: fmt.Sprintf("value-%d", p.calls)}}, &query.Total{Value: 1, Exact: true}, nil
}

var _ = Describe("filter value cache", func() {
	It("reuses stable values and refreshes them explicitly", func() {
		provider := &cachedLookupProvider{typ: "opensearch"}
		query.RegisterProvider(provider)
		request := query.FilterValueLookupRequest{
			Profile: query.Profile{
				Name:     "cache lookup",
				Provider: query.ProviderConfig{Type: provider.typ},
				Columns:  []query.ColumnDef{{Name: "status"}},
			},
			Key: "filter.status", Limit: 20,
		}

		first, total, err := query.LookupFilterValues(querycontext.New(), request)
		Expect(err).NotTo(HaveOccurred())
		second, _, err := query.LookupFilterValues(querycontext.New(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(first).To(Equal([]query.FilterOption{{Value: "value-1"}}))
		Expect(second).To(Equal(first))
		Expect(total).To(Equal(&query.Total{Value: 1, Exact: true}))
		Expect(provider.calls).To(Equal(1))

		request.Inspection.Refresh = true
		refreshed, _, err := query.LookupFilterValues(querycontext.New(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(refreshed).To(Equal([]query.FilterOption{{Value: "value-2"}}))
		Expect(provider.calls).To(Equal(2))
	})

	It("isolates entries by resolver scope", func() {
		provider := &cachedLookupProvider{typ: "opensearch"}
		query.RegisterProvider(provider)
		request := query.FilterValueLookupRequest{
			Profile: query.Profile{
				Name:     "scoped lookup",
				Provider: query.ProviderConfig{Type: provider.typ},
				Columns:  []query.ColumnDef{{Name: "status"}},
			},
			Key: "filter.status", Limit: 20,
		}
		first := querycontext.New().WithConnectionResolver(func(string) (*models.Connection, error) {
			return nil, nil
		})
		second := querycontext.New().WithConnectionResolver(func(string) (*models.Connection, error) {
			return nil, nil
		})

		_, _, err := query.LookupFilterValues(first, request)
		Expect(err).NotTo(HaveOccurred())
		_, _, err = query.LookupFilterValues(second, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(provider.calls).To(Equal(2))
	})

	It("records a cache miss as a provider operation", func() {
		original, err := query.GetProvider("opensearch")
		Expect(err).NotTo(HaveOccurred())
		provider := &cachedLookupProvider{typ: "opensearch"}
		query.RegisterProvider(provider)
		DeferCleanup(func() { query.RegisterProvider(original) })
		recorder := query.NewRecorder(query.RecorderOptions{ID: "filter-values"})
		request := query.FilterValueLookupRequest{
			Profile: query.Profile{
				Name:     "recorded lookup",
				Provider: query.ProviderConfig{Type: provider.typ},
				Columns:  []query.ColumnDef{{Name: "status"}},
			},
			Key: "filter.status", Limit: 20,
		}

		_, _, err = query.LookupFilterValues(query.WithRecorder(querycontext.New(), recorder), request)
		Expect(err).NotTo(HaveOccurred())
		recorder.Finish(query.FinishOptions{Status: 200})

		Expect(recorder.Summary().Counts.Operations).To(Equal(1))
		Expect(recorder.Detail().Operations).To(HaveLen(1))
		Expect(recorder.Detail().Operations[0].Request.Query).To(Equal("SELECT status FROM lookup_source"))
	})
})
