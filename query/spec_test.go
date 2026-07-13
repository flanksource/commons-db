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
