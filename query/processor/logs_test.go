package processor_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/processor"
)

var _ = Describe("logs.parse", func() {
	ctx := context.New()

	DescribeTable("turns structured log bodies into canonical log rows",
		func(preset, input string, expected query.Row) {
			resolved, err := query.ProcessorSpec{Use: preset}.Resolve()
			Expect(err).ToNot(HaveOccurred())

			rows, err := processor.ApplyLogParse([]query.Row{{
				"timestamp": "2026-08-13T10:15:00Z",
				"pod":       "billing-api-7f9",
				"message":   input,
			}}, processor.LogParseConfig{
				Format: resolved.Config["format"].(string),
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(rows).To(Equal([]query.Row{expected}))
		},
		Entry("JSON", "logs.json",
			`{"level":"error","msg":"request failed","request_id":"req-41","pod":"spoofed"}`,
			query.Row{
				"timestamp":  "2026-08-13T10:15:00Z",
				"pod":        "billing-api-7f9",
				"message":    "request failed",
				"severity":   "error",
				"request_id": "req-41",
				"hash":       "request failed",
			}),
		Entry("logfmt", "logs.logfmt",
			`level=warning msg="retrying request" attempt=3`,
			query.Row{
				"timestamp": "2026-08-13T10:15:00Z",
				"pod":       "billing-api-7f9",
				"message":   "retrying request",
				"severity":  "warning",
				"attempt":   "3",
				"hash":      "retrying request",
			}),
	)

	It("can read a raw body from a different column", func() {
		rows, err := processor.ApplyLogParse([]query.Row{{"body": `{"msg":"started","logger":"main"}`}}, processor.LogParseConfig{
			Format: "json",
			Column: "body",
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(Equal([]query.Row{{
			"body":    `{"msg":"started","logger":"main"}`,
			"message": "started",
			"source":  "main",
			"hash":    "started",
		}}))
	})

	It("leaves a non-matching body intact so JSON parsing can precede a Java multiline merge", func() {
		rows, err := processor.ApplyLogParse([]query.Row{{"message": "\tat com.acme.Worker.run(Worker.java:42)"}}, processor.LogParseConfig{
			Format: "json",
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(rows[0]["message"]).To(Equal("\tat com.acme.Worker.run(Worker.java:42)"))
	})

	It("composes JSON parsing with the Java stack-trace helper", func() {
		parsed, err := processor.ApplyLogParse([]query.Row{
			logRow(time.Millisecond, "\tat com.acme.Worker.run(Worker.java:42)", "pod", "worker-7f9"),
			logRow(0, `{"level":"error","msg":"com.acme.WorkException: failed"}`, "pod", "worker-7f9"),
		}, processor.LogParseConfig{Format: "json"})
		Expect(err).ToNot(HaveOccurred())

		merged, err := processor.ApplyBatch(ctx, parsed, javaLibraryConfig())
		Expect(err).ToNot(HaveOccurred())
		Expect(merged).To(HaveLen(1))
		Expect(merged[0]).To(SatisfyAll(
			HaveKeyWithValue("message", "com.acme.WorkException: failed\n\tat com.acme.Worker.run(Worker.java:42)"),
			HaveKeyWithValue("exception", "com.acme.WorkException"),
			HaveKeyWithValue("severity", "error"),
		))
	})

	It("rejects an unsupported format", func() {
		_, err := processor.ApplyLogParse([]query.Row{{"message": "plain"}}, processor.LogParseConfig{Format: "ndjson"})

		Expect(err).To(MatchError(ContainSubstring(`log format "ndjson"`)))
	})

	It("reports the row and column when the configured body is absent", func() {
		_, err := processor.ApplyLogParse([]query.Row{{"message": "ok"}, {"body": "missing message"}}, processor.LogParseConfig{})

		Expect(err).To(MatchError(ContainSubstring(`row 1 has no "message" column`)))
	})

	It("runs page by page without changing paging metadata", func() {
		registered, err := query.GetProcessor("logs.parse")
		Expect(err).ToNot(HaveOccurred())
		pageProcessor, ok := registered.(query.PageProcessor)
		Expect(ok).To(BeTrue())

		total := query.Total{Value: 8, Exact: true}
		page, err := pageProcessor.ProcessPage(ctx, query.ProcessorSpec{Config: map[string]any{"format": "json"}}, query.Page{
			Rows:    []query.Row{{"message": `{"msg":"ready"}`}},
			Styles:  []map[string]string{{"message": "text-green-500"}},
			HasMore: true,
			Total:   &total,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(page.Rows[0]).To(HaveKeyWithValue("message", "ready"))
		Expect(page.Styles).To(Equal([]map[string]string{{"message": "text-green-500"}}))
		Expect(page.HasMore).To(BeTrue())
		Expect(page.Total).To(Equal(&total))

		streamable, err := query.StreamableProcessors([]query.ProcessorSpec{{Use: "logs.json"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(streamable).To(BeTrue())
	})
})
