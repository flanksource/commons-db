package projections

import (
	"fmt"
	"time"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	ginkgo "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var testNow = time.Date(2026, 11, 16, 10, 30, 0, 0, time.UTC)

type fakeProfileRunner struct {
	result   ProfileRunResult
	requests []ProfileRunRequest
}

func (f *fakeProfileRunner) RunProfile(_ context.Context, request ProfileRunRequest) (ProfileRunResult, error) {
	f.requests = append(f.requests, request)
	return f.result, nil
}

func profileProjection(query ProjectionProfileQuery) Projection {
	p := execProjection("")
	p.Spec.Source.Query = ProjectionQuery{Profile: &query}
	return p
}

var _ = ginkgo.Describe("Profile projection source", func() {
	const windowStart, windowEnd = "2026-11-01T00:00:00Z", "2026-11-02T00:00:00Z"
	start, _ := time.Parse(time.RFC3339, windowStart)
	end, _ := time.Parse(time.RFC3339, windowEnd)
	windowed := ProjectionProfileQuery{Name: "env-metrics", All: true, Params: map[string]string{"start": windowStart, "end": windowEnd}}

	for _, test := range []struct {
		name    string
		query   ProjectionProfileQuery
		message string
	}{
		{"a missing name", ProjectionProfileQuery{}, "profile.name is required"},
		{"an invalid param template", ProjectionProfileQuery{Name: "p", Params: map[string]string{"start": "{{.context"}}, `profile.params["start"]`},
		{"an invalid selection", ProjectionProfileQuery{Name: "p", Select: "$["}, "source select"},
		{"an invalid mode", ProjectionProfileQuery{Name: "p", Mode: "always"}, "profile.mode must be"},
		{"per target without a target", ProjectionProfileQuery{Name: "p", Mode: "perTarget"}, "require spec.target"},
	} {
		ginkgo.It("rejects "+test.name, func() {
			Expect(profileProjection(test.query).validate()).To(MatchError(ContainSubstring(test.message)))
		})
	}

	ginkgo.It("rejects a profile beside another source", func() {
		p := execProjection("printf '[]'")
		p.Spec.Source.Query.Profile = &ProjectionProfileQuery{Name: "p"}
		Expect(p.validate()).To(MatchError(ContainSubstring("exactly one of exec, http or profile")))
	})

	ginkgo.It("renders params from context, runs the profile and selects JSON rows", func() {
		runner := &fakeProfileRunner{result: ProfileRunResult{Rows: []map[string]any{
			{"metric": "cpu", "values": []map[string]any{{"avg": 2}}},
		}}}
		p := profileProjection(ProjectionProfileQuery{Name: "env-metrics", All: true,
			Params: map[string]string{"instance": "{{.context.name}}-db"}, Select: "$[0].values"})
		rows, err := queryProfileProjection(context.New(), p, map[string]any{"name": "staging"}, profileSource{runner: runner, now: testNow})
		Expect(err).NotTo(HaveOccurred())
		Expect(runner.requests).To(Equal([]ProfileRunRequest{{Name: "env-metrics", All: true, Params: map[string]string{"instance": "staging-db"}}}))
		Expect(rows).To(Equal([]map[string]any{{"avg": float64(2)}}))
	})

	ginkgo.It("runs once per eligible target and wraps the rows as source.body", func() {
		dir := ginkgo.GinkgoT().TempDir()
		runner := &fakeProfileRunner{result: ProfileRunResult{Rows: []map[string]any{{"state": "ready"}}}}
		p := profileProjection(ProjectionProfileQuery{Name: "facts", Mode: "perTarget", Params: map[string]string{"id": "{{.target.id}}"}})
		p.Spec.Target = &ProjectionTarget{Path: writeProjectionTestFile(dir, "target.yaml", "entries: [{id: api, active: true}, {id: old, active: false}]\n"), Select: "$.entries[*]", Filter: "target.active"}
		rows, err := queryProfileProjection(context.New(), p, map[string]any{}, profileSource{runner: runner, now: testNow})
		Expect(err).NotTo(HaveOccurred())
		Expect(runner.requests).To(Equal([]ProfileRunRequest{{Name: "facts", Params: map[string]string{"id": "api"}}}))
		Expect(rows).To(Equal([]map[string]any{{"id": "api", "active": true, "body": []any{map[string]any{"state": "ready"}}}}))
	})

	sources := func(from, to time.Time) *query.MetricMetadata {
		return &query.MetricMetadata{Sources: []query.MetricSource{{Expression: "cpu", Start: from, End: to}}}
	}
	for _, test := range []struct {
		name    string
		result  ProfileRunResult
		message string
	}{
		{"a truncated result", ProfileRunResult{Truncated: true}, "incomplete"},
		{"a result with more pages", ProfileRunResult{HasMore: true}, "incomplete"},
		{"metric notices", ProfileRunResult{Metrics: &query.MetricMetadata{Notices: []query.QueryNotice{{Severity: "warning", Message: "series dropped"}}}}, "series dropped"},
		{"a source range other than the requested window", ProfileRunResult{Metrics: sources(start, end.Add(-time.Minute))}, "metrics.sources[0].end"},
	} {
		ginkgo.It("fails the source on "+test.name, func() {
			runner := &fakeProfileRunner{result: test.result}
			_, err := queryProfileProjection(context.New(), profileProjection(windowed), map[string]any{}, profileSource{runner: runner, now: testNow})
			Expect(err).To(MatchError(ContainSubstring(test.message)))
		})
	}

	ginkgo.It("accepts sources whose range is the requested window in another zone", func() {
		zone := time.FixedZone("SAST", 2*60*60)
		runner := &fakeProfileRunner{result: ProfileRunResult{Rows: []map[string]any{{"metric": "cpu"}}, Metrics: sources(start.In(zone), end.In(zone))}}
		rows, err := queryProfileProjection(context.New(), profileProjection(windowed), map[string]any{}, profileSource{runner: runner, now: testNow})
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(Equal([]map[string]any{{"metric": "cpu"}}))
	})

	ginkgo.Describe("datemath windows", func() {
		relative := ProjectionProfileQuery{Name: "env-metrics", All: true, Params: map[string]string{"start": "now-1h", "end": "now"}}
		const resolvedStart, resolvedEnd = "2026-11-16T09:30:00Z", "2026-11-16T10:30:00Z"
		wantStart, _ := time.Parse(time.RFC3339, resolvedStart)
		wantEnd, _ := time.Parse(time.RFC3339, resolvedEnd)

		ginkgo.It("resolves now-1h and now once against the source's now and passes RFC3339 instants", func() {
			runner := &fakeProfileRunner{}
			_, err := queryProfileProjection(context.New(), profileProjection(relative), map[string]any{}, profileSource{runner: runner, now: testNow})
			Expect(err).NotTo(HaveOccurred())
			Expect(runner.requests).To(Equal([]ProfileRunRequest{{Name: "env-metrics", All: true, Params: map[string]string{"start": resolvedStart, "end": resolvedEnd}}}))
		})

		ginkgo.It("passes the gate when metric sources cover the resolved window", func() {
			runner := &fakeProfileRunner{result: ProfileRunResult{Metrics: sources(wantStart, wantEnd)}}
			_, err := queryProfileProjection(context.New(), profileProjection(relative), map[string]any{}, profileSource{runner: runner, now: testNow})
			Expect(err).NotTo(HaveOccurred())
		})

		ginkgo.It("fails the gate when a metric source is one second off the resolved window", func() {
			runner := &fakeProfileRunner{result: ProfileRunResult{Metrics: sources(wantStart.Add(time.Second), wantEnd)}}
			_, err := queryProfileProjection(context.New(), profileProjection(relative), map[string]any{}, profileSource{runner: runner, now: testNow})
			Expect(err).To(MatchError(ContainSubstring("metrics.sources[0].start")))
		})

		ginkgo.It("fails a source whose start is neither RFC3339 nor datemath, naming the param and value", func() {
			garbage := ProjectionProfileQuery{Name: "env-metrics", Params: map[string]string{"start": "{{.context.name}}"}}
			_, err := queryProfileProjection(context.New(), profileProjection(garbage), map[string]any{"name": "yesterday-ish"}, profileSource{runner: &fakeProfileRunner{}, now: testNow})
			Expect(err).To(MatchError(ContainSubstring(`param start="yesterday-ish"`)))
		})

		ginkgo.It("verify rejects a literal garbage end but only template-parses a templated one", func() {
			Expect(profileProjection(ProjectionProfileQuery{Name: "p", Params: map[string]string{"end": "tomorrow"}}).validate()).To(MatchError(ContainSubstring(`param end="tomorrow"`)))
			Expect(profileProjection(ProjectionProfileQuery{Name: "p", Params: map[string]string{"end": "now-24h/h", "start": "{{.context.from}}"}}).validate()).To(Succeed())
		})
	})

	ginkgo.It("refuses to run a profile source without a configured runner, before any source runs", func() {
		dir := ginkgo.GinkgoT().TempDir()
		first := execProjection("touch should-not-exist")
		first.manifest = dir + "/projection.yaml"
		second := profileProjection(ProjectionProfileQuery{Name: "env-metrics"})
		second.Metadata.Name = "metrics"
		_, err := Run(context.New(), []Projection{first, second}, Options{})
		Expect(err).To(MatchError(fmt.Sprintf("projection %s: spec.source.query.profile requires a profile runner (Options.Profiles)", "metrics")))
		Expect(dir + "/should-not-exist").NotTo(BeAnExistingFile())
	})
})
