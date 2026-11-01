package query_test

import (
	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// statefulPageProcessor carries a fixed memory forward from every page.
type statefulPageProcessor struct{ kind string }

func (p statefulPageProcessor) Type() string { return p.kind }

func (statefulPageProcessor) Process(_ context.Context, _ query.ProcessorSpec, in *query.Result) (*query.Result, error) {
	return in, nil
}

func (statefulPageProcessor) ProcessPage(_ context.Context, _ query.ProcessorSpec, page query.Page, _ []byte) (query.Page, []byte, error) {
	return page, []byte("processor-memory"), nil
}

func processedState(specs []query.ProcessorSpec, carried map[string][]byte, page query.Page) (map[string][]byte, error) {
	pages := func(yield func(query.Page, error) bool) { yield(page, nil) }
	for processed, err := range query.ProcessPages(context.Context{}, specs, carried, pages) {
		return processed.State, err
	}
	Fail("ProcessPages yielded nothing")
	return nil, nil
}

var _ = Describe("ProcessPages provider state", func() {
	const stateful = "test.stateful-page"
	BeforeEach(func() {
		query.RegisterProcessor(statefulPageProcessor{kind: stateful})
		query.RegisterProcessor(statefulPageProcessor{kind: query.ProviderStateKey})
	})
	specs := []query.ProcessorSpec{{Type: stateful}}

	It("keeps the provider's position beside the processors' memory", func() {
		state, err := processedState(specs, nil, query.Page{
			HasMore: true, State: map[string][]byte{query.ProviderStateKey: []byte("partition-positions")},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(state).To(Equal(map[string][]byte{
			query.ProviderStateKey: []byte("partition-positions"),
			stateful:               []byte("processor-memory"),
		}))
	})

	It("does not re-emit the provider position the request carried in", func() {
		carried := map[string][]byte{query.ProviderStateKey: []byte("previous-positions")}
		state, err := processedState(specs, carried, query.Page{HasMore: true})
		Expect(err).ToNot(HaveOccurred())
		Expect(state).To(Equal(map[string][]byte{stateful: []byte("processor-memory")}))
	})

	It("refuses a processor labelled with the provider's key", func() {
		_, err := processedState([]query.ProcessorSpec{{Type: query.ProviderStateKey}}, nil, query.Page{HasMore: true})
		Expect(err).To(MatchError(ContainSubstring("reserved for the provider's cursor state")))
	})
})
