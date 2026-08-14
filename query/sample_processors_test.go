package query

import (
	"maps"

	dbcontext "github.com/flanksource/commons-db/context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type samplePreviewProcessor struct{}

func (samplePreviewProcessor) Type() string { return "sample.preview" }

func (samplePreviewProcessor) Process(_ dbcontext.Context, _ ProcessorSpec, in *Result) (*Result, error) {
	row := maps.Clone(in.Rows[0])
	row["processed"] = true
	row["count"] = len(in.Rows)
	return &Result{Profile: in.Profile, Rows: []Row{row}}, nil
}

var _ = Describe("Sample processor previews", func() {
	var original Provider
	var registered bool
	var originalProcessor Processor
	var processorRegistered bool

	BeforeEach(func() {
		original, registered = providerRegistry["postgres"]
		originalProcessor, processorRegistered = processorRegistry["sample.preview"]
		providerRegistry["postgres"] = sampleTestProvider{rows: []Row{
			{"message": "first"},
			{"message": "second"},
		}}
		RegisterProcessor(samplePreviewProcessor{})
	})

	AfterEach(func() {
		if registered {
			providerRegistry["postgres"] = original
		} else {
			delete(providerRegistry, "postgres")
		}
		if processorRegistered {
			processorRegistry["sample.preview"] = originalProcessor
		} else {
			delete(processorRegistry, "sample.preview")
		}
	})

	It("returns the bounded source rows and every processor stage", func() {
		result, err := Sample(dbcontext.New(), Profile{
			Name:       "processor-preview",
			Provider:   ProviderConfig{Type: "postgres"},
			Query:      "SELECT message FROM logs",
			Processors: []ProcessorSpec{{Type: "sample.preview"}},
		}, SampleOptions{Page: PageRequest{Limit: 2}, PreviewProcessors: true})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.ProcessorPreview).NotTo(BeNil())
		Expect(result.ProcessorPreview.Input).To(Equal([]Row{
			{"message": "first"},
			{"message": "second"},
		}))
		Expect(result.ProcessorPreview.Stages).To(HaveLen(1))
		stage := result.ProcessorPreview.Stages[0]
		Expect(stage.Index).To(Equal(0))
		Expect(stage.Label).To(Equal("sample.preview"))
		Expect(stage.Type).To(Equal("sample.preview"))
		Expect(stage.RowsIn).To(Equal(2))
		Expect(stage.RowsOut).To(Equal(1))
		Expect(result.ProcessorPreview.Stages[0].Rows).To(Equal([]Row{{
			"message": "first", "processed": true, "count": 2,
		}}))
		Expect(result.Rows).To(Equal(result.ProcessorPreview.Stages[0].Rows))
		Expect(result.Columns).To(ContainElements(
			ColumnDef{Name: "processed", Type: ColumnTypeBoolean},
			ColumnDef{Name: "count", Type: ColumnTypeNumber},
		))
	})

	It("continues to bypass processors for an ordinary sample", func() {
		result, err := Sample(dbcontext.New(), Profile{
			Name:       "raw-sample",
			Provider:   ProviderConfig{Type: "postgres"},
			Query:      "SELECT message FROM logs",
			Processors: []ProcessorSpec{{Type: "sample.preview"}},
		}, SampleOptions{Page: PageRequest{Limit: 2}})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.ProcessorPreview).To(BeNil())
		Expect(result.Rows).To(HaveLen(2))
		Expect(result.Rows[0]).NotTo(HaveKey("processed"))
	})
})
