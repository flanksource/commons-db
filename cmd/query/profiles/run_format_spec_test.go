package profiles

import (
	"encoding/json"

	"github.com/flanksource/clicky"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Profile machine output", func() {
	It("retains numeric zeros, metric notices and completeness in the JSON envelope", func() {
		result := &RunResult{
			Profile: "idle", Rows: []query.Row{{"min": 0, "p50": 0, "samples": 360}},
			Metrics:   &query.MetricMetadata{Notices: []query.QueryNotice{{Severity: "warning", Message: "partial upstream data"}}},
			Truncated: true,
		}
		for _, render := range []func() (string, error){
			func() (string, error) { return result.Render("json") },
			func() (string, error) { return clicky.Format(result, clicky.FormatOptions{Format: "json"}) },
		} {
			output, err := render()
			Expect(err).NotTo(HaveOccurred())
			var decoded map[string]any
			Expect(json.Unmarshal([]byte(output), &decoded)).To(Succeed())
			Expect(decoded).To(HaveKeyWithValue("profile", "idle"))
			Expect(decoded).To(HaveKeyWithValue("truncated", true))
			Expect(decoded["rows"]).To(Equal([]any{map[string]any{"min": float64(0), "p50": float64(0), "samples": float64(360)}}))
			Expect(decoded["metrics"]).To(Equal(map[string]any{"notices": []any{map[string]any{"severity": "warning", "message": "partial upstream data"}}}))
		}
	})
})
