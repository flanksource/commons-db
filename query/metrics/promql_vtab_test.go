package metrics

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("prometheus virtual table", func() {
	window := Range{Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2024, 1, 1, 1, 0, 0, 0, time.UTC), Step: time.Minute}
	type call struct {
		Expr  string
		Range Range
	}
	recording := func(calls *[]call) *RangeQuery {
		return &RangeQuery{Window: window, Fetch: func(expr string, r Range, emit func(Sample) error) error {
			*calls = append(*calls, call{Expr: expr, Range: r})
			for i, pod := range []string{"api", "web"} {
				if err := emit(Sample{Time: r.Start, Value: float64(i + 1), Labels: map[string]string{"__name__": "cpu", "pod": pod}}); err != nil {
					return err
				}
			}
			return nil
		}}
	}

	It("evaluates the expression over the request window and exposes labels as JSON", func() {
		var calls []call
		rows, err := Execute(context.Background(), Request{SQL: `SELECT time, metric_name, json_extract(labels,'$.pod') AS pod, labels, value FROM prometheus('sum by (pod)(rate(cpu[5m]))') ORDER BY pod`, Range: recording(&calls)}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal([]call{{Expr: "sum by (pod)(rate(cpu[5m]))", Range: window}}))
		Expect(rows).To(Equal([]map[string]any{
			{"time": "2024-01-01T00:00:00.000000000Z", "metric_name": "cpu", "pod": "api", "labels": `{"pod":"api"}`, "value": float64(1)},
			{"time": "2024-01-01T00:00:00.000000000Z", "metric_name": "cpu", "pod": "web", "labels": `{"pod":"web"}`, "value": float64(2)},
		}))
	})

	It("takes explicit start, end and step and a bound query", func() {
		var calls []call
		_, err := Execute(context.Background(), Request{
			SQL:   `SELECT count(*) AS n FROM prometheus(__cdb_query_param_0__, '2024-02-01T00:00:00Z', '2024-02-01T00:10:00Z', '30s')`,
			Args:  []any{"up"},
			Range: recording(&calls),
		}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal([]call{{Expr: "up", Range: Range{Start: time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2024, 2, 1, 0, 10, 0, 0, time.UTC), Step: 30 * time.Second}}}))
	})

	It("joins with a metric table in one statement", func() {
		var calls []call
		rows, err := Execute(context.Background(), Request{SQL: `SELECT p.value AS p, m.value AS m FROM prometheus('x') AS p JOIN cpu AS m ON m.time = p.time WHERE json_extract(p.labels,'$.pod') = 'api'`, Range: recording(&calls)}, func(_ Source, emit func(Sample) error) error {
			return emit(Sample{Time: window.Start, Value: 7})
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(Equal([]map[string]any{{"p": float64(1), "m": float64(7)}}))
	})

	It("requires a query", func() {
		var calls []call
		_, err := Execute(context.Background(), Request{SQL: `SELECT * FROM prometheus`, Range: recording(&calls)}, nil)
		Expect(err).To(MatchError(ContainSubstring("prometheus requires a query")))
		Expect(calls).To(BeEmpty())
	})

	It("fails when the provider supplies no range queries", func() {
		_, err := Execute(context.Background(), Request{SQL: `SELECT * FROM prometheus('up')`}, nil)
		Expect(err).To(MatchError(ContainSubstring("prometheus(...) is not available")))
	})

	It("propagates a fetch failure", func() {
		_, err := Execute(context.Background(), Request{SQL: `SELECT * FROM prometheus('up')`, Range: &RangeQuery{Window: window, Fetch: func(string, Range, func(Sample) error) error {
			return errBoom
		}}}, nil)
		Expect(err).To(MatchError(ContainSubstring(errBoom.Error())))
	})
})
