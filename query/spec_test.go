package query_test

import (
	"time"

	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

var _ = Describe("Profile trace/top specs", func() {
	It("unmarshals a trace block and derives the trace kind", func() {
		var p query.Profile
		Expect(yaml.Unmarshal([]byte(`
profile: exec trace
provider:
  type: fake-stream
trace:
  maxDuration: 5m
  maxEvents: 500
`), &p)).To(Succeed())

		Expect(p.Kind()).To(Equal(query.KindTrace))
		Expect(p.Trace.MaxDuration.Duration).To(Equal(5 * time.Minute))
		Expect(p.Trace.MaxEvents).To(Equal(500))
		Expect(p.ValidateKind()).To(Succeed())
	})

	It("unmarshals a top block and derives the top kind", func() {
		var p query.Profile
		Expect(yaml.Unmarshal([]byte(`
profile: pg activity
provider:
  type: sql
top:
  interval: 2s
  sortBy: duration
  limit: 20
`), &p)).To(Succeed())

		Expect(p.Kind()).To(Equal(query.KindTop))
		Expect(p.Top.Interval.Duration).To(Equal(2 * time.Second))
		Expect(p.Top.SortBy).To(Equal("duration"))
		Expect(p.Top.Limit).To(Equal(20))
	})

	It("defaults to the query kind when neither block is set", func() {
		p := query.Profile{Name: "plain"}
		Expect(p.Kind()).To(Equal(query.KindQuery))
		Expect(p.ValidateKind()).To(Succeed())
	})

	It("rejects a profile declaring both trace and top", func() {
		p := query.Profile{Name: "both", Trace: &query.TraceSpec{}, Top: &query.TopSpec{}}
		err := p.ValidateKind()
		Expect(err).To(MatchError(ContainSubstring("both")))
	})

	// The provider rejects this pair at execution, which is far too late: the
	// edit form would persist a profile that only fails the next time someone
	// runs it. Validate has to refuse it at save.
	It("rejects a profile that sets both the raw query and a structured search", func() {
		p := query.Profile{
			Name:  "both-queries",
			Query: `{"query":{"match_all":{}}}`,
			Provider: query.ProviderConfig{Type: "opensearch",
				Options: map[string]any{"search": map[string]any{"query": map[string]any{"op": "match_all"}}}},
		}
		Expect(p.Validate()).To(MatchError(ContainSubstring("mutually exclusive")))
	})

	It("accepts a profile that sets only the structured search", func() {
		p := query.Profile{
			Name: "search-only",
			Provider: query.ProviderConfig{Type: "opensearch",
				Options: map[string]any{"search": map[string]any{"query": map[string]any{"op": "match_all"}}}},
		}
		Expect(p.Validate()).To(Succeed())
	})

	// The caps are only read when the profile runs, so an unreachable pair would
	// otherwise be stored and surface as an odd page much later.
	It("rejects a profile whose row limits contradict each other", func() {
		p := query.Profile{Name: "big-pages", Limits: &query.RowLimits{PageSize: 5000}}
		Expect(p.Validate()).To(MatchError(ContainSubstring(`profile "big-pages": limits.pageSize 5000`)))
	})

	It("accepts a profile that raises its own export ceiling", func() {
		p := query.Profile{Name: "exporter", Limits: &query.RowLimits{MaxExportRows: 250_000}}
		Expect(p.Validate()).To(Succeed())
		Expect(p.RowLimits().MaxExportRows).To(Equal(250_000))
	})

	It("applies defaults for zero-valued trace limits", func() {
		s := query.TraceSpec{}
		Expect(s.DurationLimit()).To(Equal(15 * time.Minute))
		Expect(s.EventLimit()).To(Equal(10000))
	})

	It("keeps explicit trace limits", func() {
		s := query.TraceSpec{MaxEvents: 42}
		s.MaxDuration.Duration = time.Minute
		Expect(s.DurationLimit()).To(Equal(time.Minute))
		Expect(s.EventLimit()).To(Equal(42))
	})

	It("defaults and floors the top interval", func() {
		Expect(query.TopSpec{}.TickInterval()).To(Equal(5 * time.Second))

		fast := query.TopSpec{}
		fast.Interval.Duration = 100 * time.Millisecond
		Expect(fast.TickInterval()).To(Equal(time.Second))

		Expect(query.TopSpec{}.DurationLimit()).To(Equal(15 * time.Minute))
	})
})

var _ = Describe("Profile column validation", func() {
	It("accepts canonical units on explicitly numeric columns", func() {
		p := query.Profile{Name: "metrics", Columns: []query.ColumnDef{{
			Name: "ratio", Type: query.ColumnTypeNumber, Format: "currency", Unit: "percentunit",
		}}}
		Expect(p.Validate()).To(Succeed())
	})

	DescribeTable("rejects invalid column metadata",
		func(column query.ColumnDef, message string) {
			p := query.Profile{Name: "metrics", Columns: []query.ColumnDef{column}}
			Expect(p.Validate()).To(MatchError(ContainSubstring(message)))
		},
		Entry("unknown format", query.ColumnDef{Name: "ratio", Type: query.ColumnTypeNumber, Format: "custom"}, `column "ratio" format "custom"`),
		Entry("unknown unit", query.ColumnDef{Name: "ratio", Type: query.ColumnTypeNumber, Unit: "requests"}, `column "ratio" unit "requests"`),
		Entry("unit without numeric type", query.ColumnDef{Name: "ratio", Unit: "percentunit"}, `column "ratio" unit requires type number, duration, or bytes`),
		Entry("unit on string", query.ColumnDef{Name: "ratio", Type: query.ColumnTypeString, Unit: "percentunit"}, `column "ratio" unit requires type number, duration, or bytes`),
		Entry("source plus CEL", query.ColumnDef{Name: "ratio", Source: "raw_ratio", CEL: "row.raw_ratio / 100"}, `column "ratio" cannot set both source and cel`),
	)
})
