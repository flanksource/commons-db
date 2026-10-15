// Specs for a window's downloads and summaries: every kind exports NDJSON and
// counts its records; a kind that is an Exporter or a Summarizer adds its own.

package tracing_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/tracingtest"
)

var csvFormat = tracing.ExportFormat{Name: "csv", Ext: "csv", MIME: "text/csv"}

// tallied emits its params' count of ticks, exports them as CSV and sums them.
type tallied struct{ formats []tracing.ExportFormat }

func (tallied) Params() tickParams { return tickParams{Count: 3} }

func (tallied) Schema() recordresults.ResultType[tick] {
	return recordresults.ResultType[tick]{Title: "Tallied", TimeColumn: "at"}
}

func (tallied) Handle(ctx dbcontext.Context, p tickParams, records tracing.Emitter[tick], _ tracing.Records[tick]) error {
	return emitTicks(ctx, records, p.Count)
}

func (t tallied) Formats() []tracing.ExportFormat { return t.formats }

func (tallied) Export(format string, rows iter.Seq[tick], w io.Writer) error {
	if _, err := fmt.Fprintln(w, "n"); err != nil {
		return err
	}
	for row := range rows {
		if _, err := fmt.Fprintln(w, row.N); err != nil {
			return err
		}
	}
	return nil
}

// Summarize sums the ticks, and claims a count the runtime corrects.
func (tallied) Summarize(rows iter.Seq[tick]) (map[string]any, error) {
	sum := 0
	for row := range rows {
		sum += row.N
	}
	return map[string]any{"sum": sum, "count": -1}, nil
}

var _ = Describe("a window's exports and summaries", func() {
	var env tracingtest.Env
	var ref tracing.TraceRef

	capture := func(kind string) tracing.TraceRef {
		info := tracingtest.Ended(env.Start(kind, ""))
		Expect(info.Error).To(BeEmpty())
		return tracing.TraceRef{Kind: kind, Stream: info.Events.Stream}
	}

	BeforeEach(func() {
		env = tracingtest.NewEnv(map[string]tracing.TracePlugin{
			"tallied": tracing.NewHandler[tickParams, tick](tallied{formats: []tracing.ExportFormat{csvFormat}}, tracing.Capabilities{Historical: true}),
			"ticks": tracing.NewHandler[tickParams, tick](scripted{handle: func(ctx dbcontext.Context, p tickParams, records tracing.Emitter[tick], _ tracing.Records[tick]) error {
				return emitTicks(ctx, records, p.Count)
			}}, tracing.Capabilities{Historical: true}),
		})
		ref = capture("tallied")
	})

	export := func(ref tracing.TraceRef, format string) (string, error) {
		var out bytes.Buffer
		err := env.Runtime.Export(context.Background(), ref, format, &out)
		return out.String(), err
	}

	It("export any kind's window as NDJSON, one stored row per line", func() {
		ref := capture("ticks")
		ref.AfterSeq, ref.ToSeq = 1, 2
		out, err := export(ref, "ndjson")
		Expect(err).ToNot(HaveOccurred())
		lines := strings.Split(strings.TrimSpace(out), "\n")
		Expect(lines).To(HaveLen(1))
		var row map[string]any
		Expect(json.Unmarshal([]byte(lines[0]), &row)).To(Succeed())
		Expect(row).To(HaveKeyWithValue("n", float64(2)))
	})

	It("export through the kind's own format, refusing one it lacks", func() {
		Expect(export(ref, "csv")).To(Equal("n\n1\n2\n3\n"))
		_, err := export(ref, "har")
		Expect(err).To(MatchError(tracing.ErrUnknownFormat))
	})

	It("list the kind's formats after NDJSON, which every kind has", func() {
		kinds, err := env.Runtime.Kinds.List()
		Expect(err).ToNot(HaveOccurred())
		formats := map[string][]tracing.ExportFormat{}
		for _, kind := range kinds {
			formats[kind.Name] = kind.ExportFormats
		}
		Expect(formats).To(Equal(map[string][]tracing.ExportFormat{
			"tallied": {tracing.NDJSON, csvFormat},
			"ticks":   {tracing.NDJSON},
		}))
	})

	It("summarize a kind that has no Summarizer as its count, and one that has with its count corrected", func() {
		Expect(env.Runtime.Summarize(context.Background(), capture("ticks"))).To(Equal(map[string]any{"count": 3}))
		ref.AfterSeq = 1
		Expect(env.Runtime.Summarize(context.Background(), ref)).To(Equal(map[string]any{"sum": 5, "count": 2}))
	})

	DescribeTable("refuse to register a kind whose formats a download could not be named by",
		func(format tracing.ExportFormat, message string) {
			err := tracing.NewKinds().RegisterKind("tallied", tracing.NewHandler[tickParams, tick](tallied{formats: []tracing.ExportFormat{format}}, tracing.Capabilities{Historical: true}))
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("the built-in ndjson", tracing.ExportFormat{Name: "ndjson", Ext: "ndjson", MIME: "application/x-ndjson"}, `"ndjson" twice`),
		Entry("no extension", tracing.ExportFormat{Name: "har", MIME: "application/json"}, `format "har" needs`),
		Entry("no media type", tracing.ExportFormat{Name: "har", Ext: "har"}, `format "har" needs`),
	)
})
