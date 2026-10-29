package metrics

import (
	"context"
	"os"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMetrics(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Metric SQL") }

var _ = Describe("metric summaries", func() {
	It("pushes bound label equality values without interpolating SQL", func() {
		_, err := Execute(context.Background(), Request{SQL: `SELECT pod FROM cpu WHERE pod=__cdb_query_param_0__`, Args: []any{"acme"}, Labels: map[string]string{"pod": "pod"}}, func(s Source, _ func(Sample) error) error {
			Expect(s.Filters).To(Equal(map[string]string{"pod": "acme"}))
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
	})
	It("executes the shipped RDS summary with dimension aliases and unit conversions", func() {
		body, err := os.ReadFile("../../profiles/rds-sqlserver-metrics.yaml")
		Expect(err).NotTo(HaveOccurred())
		var profile struct {
			Query  string            `json:"query"`
			Labels map[string]string `json:"labels"`
		}
		Expect(yaml.Unmarshal(body, &profile)).To(Succeed())
		rows, err := Execute(context.Background(), Request{SQL: profile.Query, Labels: profile.Labels}, func(_ Source, emit func(Sample) error) error {
			return emit(Sample{Value: 0.01, Labels: map[string]string{"DBInstanceIdentifier": "acme-sqlserver"}})
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(7))
		for _, r := range rows {
			Expect(r["db_instance"]).To(Equal("acme-sqlserver"))
			if r["metric"] == "read_latency_period_average" {
				Expect(r["unit"]).To(Equal("ms"))
				Expect(r["p95"]).To(Equal(float64(10)))
			}
		}
	})
	It("calculates exact statistics after summing simultaneous rates", func() {
		var expressions []Source
		rows, err := Execute(context.Background(), Request{SQL: `WITH totals AS (SELECT time, pod, sum(rate(value, '5m')) AS cores FROM cpu GROUP BY time,pod) SELECT pod,min(cores) AS min,max(cores) AS max,avg(cores) AS avg,percentile(cores,95) AS p95,count(*) AS samples FROM totals GROUP BY pod`, Labels: map[string]string{"pod": "pod"}}, func(s Source, emit func(Sample) error) error {
			expressions = append(expressions, s)
			for i := 0; i < 2; i++ {
				for _, v := range []float64{0, 10} {
					if err := emit(Sample{Time: time.Unix(int64(i), 0), Value: v, Labels: map[string]string{"pod": "app", "instance": string(rune('a' + int(v)))}}); err != nil {
						return err
					}
				}
			}
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(expressions).To(HaveLen(1))
		Expect(expressions[0].Transform).To(Equal("rate"))
		Expect(expressions[0].Window).To(Equal("5m"))
		Expect(rows).To(Equal([]map[string]any{{"pod": "app", "min": float64(10), "max": float64(10), "avg": float64(10), "p95": float64(10), "samples": int64(2)}}))
	})
	It("declares missing label columns for empty sources", func() {
		rows, err := Execute(context.Background(), Request{SQL: `SELECT pod,count(value) AS samples FROM cpu GROUP BY pod`, Labels: map[string]string{"pod": "pod"}}, func(Source, func(Sample) error) error { return nil })
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(BeEmpty())
	})
	It("rejects rate after aggregation", func() {
		_, err := Execute(context.Background(), Request{SQL: `SELECT rate(sum(value),'5m') FROM cpu`}, func(Source, func(Sample) error) error { Fail("must not fetch"); return nil })
		Expect(err).To(HaveOccurred())
	})
	It("interpolates percentiles over all samples", func() {
		rows, err := Execute(context.Background(), Request{SQL: `SELECT percentile(value,95) AS p95,percentile_cont(value,0.95) AS continuous,median(value) AS p50 FROM memory`}, func(_ Source, emit func(Sample) error) error {
			for i, v := range []float64{0, 10, 20, 30} {
				if err := emit(Sample{Time: time.Unix(int64(i), 0), Value: v}); err != nil {
					return err
				}
			}
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(Equal([]map[string]any{{"p95": 28.5, "continuous": 28.5, "p50": float64(15)}}))
	})
	It("fills an explicit bucket grid without changing aggregate inputs", func() {
		rows, err := Execute(context.Background(), Request{SQL: `SELECT g.time,coalesce(s.value,0) AS value FROM time_series('2024-01-01T00:00:00Z','2024-01-01T00:03:00Z','1m') AS g LEFT JOIN cpu AS s ON s.time=g.time ORDER BY g.time`}, func(_ Source, emit func(Sample) error) error {
			return emit(Sample{Time: time.Date(2024, 1, 1, 0, 1, 0, 0, time.UTC), Value: 10})
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(3))
		Expect(rows[1]["value"]).To(Equal(float64(10)))
		Expect(rows[0]["value"]).To(Equal(int64(0)))
	})
	It("rejects mixing raw counters and transformed values", func() { _, err := Plan(`SELECT value,rate(value,'5m') FROM cpu`); Expect(err).To(HaveOccurred()) })
	It("rejects wildcard counter projections", func() { _, err := Plan(`SELECT *, rate(value,'5m') FROM cpu`); Expect(err).To(HaveOccurred()) })
	It("ignores repeated identical source samples", func() {
		rows, err := Execute(context.Background(), Request{SQL: `SELECT count(value) AS samples FROM cpu`}, func(_ Source, emit func(Sample) error) error {
			for i := 0; i < 2; i++ {
				if err := emit(Sample{Value: 10}); err != nil {
					return err
				}
			}
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(rows[0]["samples"]).To(Equal(int64(1)))
	})
	DescribeTable("validates percentile parameters", func(expression string) {
		_, err := Execute(context.Background(), Request{SQL: `SELECT ` + expression + ` FROM cpu`}, func(_ Source, emit func(Sample) error) error { return emit(Sample{Value: 10}) })
		Expect(err).To(HaveOccurred())
	}, Entry("above range", `percentile(value,101)`), Entry("null rank", `percentile(value,NULL)`), Entry("non numeric", `percentile('bad',95)`))
	It("selects first/last by timestamp", func() {
		rows, err := Execute(context.Background(), Request{SQL: `SELECT first(value,time) AS first,last(value,time) AS last FROM cpu`}, func(_ Source, emit func(Sample) error) error {
			for _, i := range []int64{2, 0, 1} {
				if err := emit(Sample{Time: time.Unix(i, 0), Value: float64(i)}); err != nil {
					return err
				}
			}
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(rows[0]).To(Equal(map[string]any{"first": float64(0), "last": float64(2)}))
	})
})
