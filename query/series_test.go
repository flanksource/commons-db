package query_test

import (
	"time"

	clickymetrics "github.com/flanksource/clicky/metrics"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ProjectSeries", func() {
	t0 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(30 * time.Second)

	It("groups metric SQL rows by label set into ascending series", func() {
		result := query.Result{Profile: "pod-cpu", Rows: []query.Row{
			{"time": "2024-01-01T00:00:30.000000000Z", "pod": "web", "value": int64(4)},
			{"time": "2024-01-01T00:00:30.000000000Z", "pod": "api", "value": 2.5},
			{"time": "2024-01-01T00:00:00.000000000Z", "pod": "api", "value": 1.5},
		}}
		series, err := query.ProjectSeries(result, query.SeriesSpec{Unit: "percent", Label: "{{.pod}} cpu"})
		Expect(err).NotTo(HaveOccurred())
		Expect(series).To(Equal([]clickymetrics.Series{
			{ID: "pod-cpu#pod=api", Source: "pod-cpu", Label: "api cpu", Labels: map[string]string{"pod": "api"}, Unit: "percent",
				Points: []clickymetrics.Point{{At: t0, Value: 1.5}, {At: t1, Value: 2.5}}},
			{ID: "pod-cpu#pod=web", Source: "pod-cpu", Label: "web cpu", Labels: map[string]string{"pod": "web"}, Unit: "percent",
				Points: []clickymetrics.Point{{At: t1, Value: 4}}},
		}))
	})

	It("reads PromQL rows by their timestamp column and groups by metric", func() {
		result := query.Result{Profile: "jvm", Rows: []query.Row{
			{"timestamp": t0, "metric": "heap", "area": "eden", "instance": "a", "value": float64(10)},
			{"timestamp": t0, "metric": "heap", "area": "old", "instance": "a", "value": float64(20)},
		}}
		series, err := query.ProjectSeries(result, query.SeriesSpec{Metric: "metric", Labels: []string{"area"}, Source: "profile:jvm?instance=a"})
		Expect(err).NotTo(HaveOccurred())
		Expect(series).To(Equal([]clickymetrics.Series{
			{ID: "profile:jvm?instance=a#area=eden,metric=heap", Source: "profile:jvm?instance=a", Labels: map[string]string{"area": "eden", "metric": "heap"}, Points: []clickymetrics.Point{{At: t0, Value: 10}}},
			{ID: "profile:jvm?instance=a#area=old,metric=heap", Source: "profile:jvm?instance=a", Labels: map[string]string{"area": "old", "metric": "heap"}, Points: []clickymetrics.Point{{At: t0, Value: 20}}},
		}))
	})

	It("names a lone unlabelled series after its source", func() {
		series, err := query.ProjectSeries(query.Result{Profile: "total", Rows: []query.Row{{"time": t0, "value": 3}}}, query.SeriesSpec{})
		Expect(err).NotTo(HaveOccurred())
		Expect(series).To(Equal([]clickymetrics.Series{{ID: "total", Source: "total", Points: []clickymetrics.Point{{At: t0, Value: 3}}}}))
	})

	It("projects nothing from no rows", func() {
		series, err := query.ProjectSeries(query.Result{Profile: "empty"}, query.SeriesSpec{})
		Expect(err).NotTo(HaveOccurred())
		Expect(series).To(BeEmpty())
	})

	DescribeTable("refuses rows it cannot plot rather than dropping them",
		func(row query.Row, spec query.SeriesSpec, message string) {
			_, err := query.ProjectSeries(query.Result{Profile: "bad", Rows: []query.Row{row}}, spec)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("non-numeric value", query.Row{"time": t0, "value": "high"}, query.SeriesSpec{}, `value column "value"`),
		Entry("null value", query.Row{"time": t0, "value": nil}, query.SeriesSpec{}, `value column "value"`),
		Entry("unparsable time", query.Row{"time": "yesterday", "value": 1}, query.SeriesSpec{}, `time column "time"`),
		Entry("no time column", query.Row{"at": t0, "value": 1}, query.SeriesSpec{}, "no time column"),
		Entry("missing declared value column", query.Row{"time": t0, "value": 1}, query.SeriesSpec{Value: "cores"}, `value column "cores"`),
		Entry("label template over an absent label", query.Row{"time": t0, "value": 1}, query.SeriesSpec{Label: "{{.pod}}"}, "label template"),
	)
})
