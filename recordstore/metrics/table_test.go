package metrics_test

import (
	"github.com/flanksource/commons-db/recordstore/metrics"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"testing"
	"time"
)

func TestMetrics(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Dynamic metric table") }

var _ = Describe("metric rows", func() {
	It("preinitializes columns and adds numeric metrics on demand while preserving zero and null", func() {
		table, err := metrics.NewDynamicMetricTable(metrics.TableOptions{Kind: "process_metrics", Metrics: []metrics.Definition{{Name: "cpu", Unit: "percent"}}})
		Expect(err).NotTo(HaveOccurred())
		row, err := table.Row(metrics.Sample{Entity: "pid@start", At: time.Unix(1, 0), Tick: 1, Values: map[string]any{"cpu": 0, "rss": uint64(1024), "unavailable": nil}})
		Expect(err).NotTo(HaveOccurred())
		Expect(row).To(HaveKeyWithValue("cpu", 0))
		Expect(row).To(HaveKeyWithValue("rss", uint64(1024)))
		Expect(table.Schema().Columns).To(HaveLen(6))
		_, err = table.Row(metrics.Sample{Entity: "pid@start", At: time.Unix(2, 0), Tick: 2, Values: map[string]any{"rss": "bad"}})
		Expect(err).To(HaveOccurred())
		Expect(table.Schema().Columns).To(HaveLen(6))
	})
	It("does not publish partially validated metric definitions", func() {
		table, err := metrics.NewDynamicMetricTable(metrics.TableOptions{Kind: "process_metrics"})
		Expect(err).NotTo(HaveOccurred())
		_, err = table.Row(metrics.Sample{Entity: "p", At: time.Unix(1, 0), Tick: 1, Definitions: []metrics.Definition{{Name: "cpu"}}, Values: map[string]any{"cpu": 1, "bad": "text"}})
		Expect(err).To(HaveOccurred())
		Expect(table.Schema().Columns).To(HaveLen(3))
	})
})
