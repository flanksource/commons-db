package query_test

import (
	"fmt"
	"maps"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type rawFirstProcessor struct{}

func (rawFirstProcessor) Type() string { return "test.raw-first" }

func (rawFirstProcessor) Process(_ context.Context, _ query.ProcessorSpec, in *query.Result) (*query.Result, error) {
	result := *in
	result.Rows = processedRows(in.Rows)
	return &result, nil
}

func (p rawFirstProcessor) ProcessPage(
	ctx context.Context,
	spec query.ProcessorSpec,
	page query.Page,
	state []byte,
) (query.Page, []byte, error) {
	result, err := p.Process(ctx, spec, &query.Result{Rows: page.Rows})
	if err != nil {
		return query.Page{}, nil, err
	}
	page.Rows = result.Rows
	return page, state, nil
}

type wholeRawFirstProcessor struct{}

func (wholeRawFirstProcessor) Type() string { return "test.whole-raw-first" }

func (wholeRawFirstProcessor) Process(_ context.Context, _ query.ProcessorSpec, in *query.Result) (*query.Result, error) {
	result := *in
	result.Rows = processedRows(in.Rows)
	return &result, nil
}

type processorRunCounter struct{}

func (processorRunCounter) Type() string { return "test.processor-run-counter" }

func (processorRunCounter) Process(_ context.Context, _ query.ProcessorSpec, in *query.Result) (*query.Result, error) {
	result := *in
	result.Rows = make([]query.Row, len(in.Rows))
	for index, input := range in.Rows {
		row := maps.Clone(input)
		if runs, ok := row["processorRuns"].(int); ok {
			row["processorRuns"] = runs + 1
		} else {
			row["processorRuns"] = 1
		}
		result.Rows[index] = row
	}
	return &result, nil
}

func processedRows(input []query.Row) []query.Row {
	rows := make([]query.Row, len(input))
	for index, inputRow := range input {
		row := maps.Clone(inputRow)
		row["processed"] = fmt.Sprintf("%v-processed", row["raw"])
		row["key"] = fmt.Sprint(row["raw"])
		row["batchSize"] = len(input)
		rows[index] = row
	}
	return rows
}

func rawFirstProfile(name, providerType, processorType string) query.Profile {
	return query.Profile{
		Name:       name,
		Provider:   query.ProviderConfig{Type: providerType},
		Processors: []query.ProcessorSpec{{Type: processorType}},
		Aliases:    []query.AliasDef{{Name: "aliased", CEL: `row.processed + "-aliased"`}},
		Ignore:     []string{"raw", "processed"},
		Filters: []query.FilterDef{{
			Name: "processed-only", Hidden: true,
			Fields: map[string]string{"processed": `row.aliased.endsWith("-processed-aliased")`},
		}},
		Columns: []query.ColumnDef{{Name: "mapped", CEL: `row.aliased + "-mapped"`}},
	}
}

var _ = Describe("processor and row mapping order", func() {
	BeforeEach(func() {
		query.RegisterProcessor(rawFirstProcessor{})
		query.RegisterProcessor(wholeRawFirstProcessor{})
		query.RegisterProcessor(processorRunCounter{})
	})

	It("runs buffered processors before aliases, filters, ignores, and columns", func() {
		query.RegisterProvider(&mockProvider{typ: "raw-first-buffered", rows: []query.Row{{"raw": "alpha"}}})

		result, err := query.Execute(context.New(), rawFirstProfile("buffered", "raw-first-buffered", "test.raw-first"))

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(Equal([]query.Row{{
			"key": "alpha", "batchSize": int64(1), "aliased": "alpha-processed-aliased", "mapped": "alpha-processed-aliased-mapped",
		}}))
	})

	It("runs page processors before aliases, filters, ignores, and columns", func() {
		query.RegisterProvider(&cappedProvider{typ: "raw-first-paged", rows: []query.Row{{"raw": "alpha"}}})
		profile := rawFirstProfile("paged", "raw-first-paged", "test.raw-first")

		rows, err := query.CollectRows(query.Rows(query.ExecutePages(context.New(), profile, query.PageRequest{Limit: 1})))

		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(Equal([]query.Row{{
			"key": "alpha", "batchSize": int64(1), "aliased": "alpha-processed-aliased", "mapped": "alpha-processed-aliased-mapped",
		}}))
	})

	It("refuses to page a processor that needs the whole result", func() {
		query.RegisterProvider(&cappedProvider{typ: "raw-first-unpageable", rows: []query.Row{{"raw": "alpha"}}})
		profile := rawFirstProfile("unpageable", "raw-first-unpageable", "test.whole-raw-first")

		_, err := query.CollectRows(query.Rows(query.ExecutePages(context.New(), profile, query.PageRequest{Limit: 1})))

		Expect(err).To(MatchError(ContainSubstring("cannot be paged")))
		Expect(err).To(MatchError(ContainSubstring("test.whole-raw-first")))
	})

	It("buffers reconciliation when either side needs a whole-result processor", func() {
		query.RegisterProvider(&mockProvider{typ: "raw-first-reconcile-source", rows: []query.Row{{"raw": "shared"}}})
		query.RegisterProvider(&mockProvider{typ: "raw-first-reconcile-dest", rows: []query.Row{{"raw": "shared"}}})
		source := query.Profile{
			Name: "source", Provider: query.ProviderConfig{Type: "raw-first-reconcile-source"},
			Order:      query.Order{{Column: "key", Unique: true}},
			Processors: []query.ProcessorSpec{{Type: "test.whole-raw-first"}},
			Columns:    []query.ColumnDef{{Name: "key", CEL: "row.key"}},
		}
		dest := source
		dest.Name = "dest"
		dest.Provider.Type = "raw-first-reconcile-dest"

		result, err := query.ReconcileProfiles(context.New(), query.ReconcileRun{
			Source: source,
			Dest:   dest,
			Config: query.ReconcileConfig{ReconcileSpec: query.ReconcileSpec{Key: query.KeySpec{Columns: []string{"key"}}}},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Mode).To(Equal(query.ReconcileBuffered))
		Expect(result.BufferedReason).To(ContainSubstring("test.whole-raw-first"))
		Expect(result.Stats.Matched).To(Equal(1))
	})

	It("materializes already-processed trace events without running processors again", func() {
		profile := query.Profile{
			Name:       "persisted-trace",
			Trace:      &query.TraceSpec{},
			Processors: []query.ProcessorSpec{{Type: "test.processor-run-counter"}},
		}

		result, err := query.MaterializeEvents(context.New(), profile, []query.Event{{Row: query.Row{"processorRuns": 1}}})

		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(Equal([]query.Row{{"processorRuns": 1}}))
	})
})
