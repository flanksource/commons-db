package providers

import (
	"time"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/tracing/xetrace"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL XEvents provider options", func() {
	It("projects every option onto the capture and drain settings", func() {
		options := sqlXEventOptions{
			SessionName: "acme_capture",
			Database:    "warehouse",
			Users:       []string{"analytics", "!sa"},
			Apps:        []string{"!go/reporting"},
			Hosts:       []string{"app-0"},
			Events:      []string{"sql_statement_completed", "sp_statement_completed"},
			Types:       []string{"SELECT", "DML"},
			Tables:      []string{"orders"},
			MinDuration: "10ms",
			Poll:        "2s",
			MaxEvents:   500,
			MaxMemoryKB: 8192,
		}

		create, drain, err := options.captureOptions()
		Expect(err).ToNot(HaveOccurred())
		Expect(create.DatabaseName).To(Equal("warehouse"))
		Expect(create.Users).To(Equal([]string{"analytics", "!sa"}))
		Expect(create.Apps).To(Equal([]string{"!go/reporting"}))
		Expect(create.Hosts).To(Equal([]string{"app-0"}))
		Expect(create.Events).To(Equal([]string{"sql_statement_completed", "sp_statement_completed"}))
		Expect(create.MinDurationMicros).To(Equal(int64(10000)))
		Expect(create.MaxEvents).To(Equal(500))
		Expect(create.MaxMemoryKB).To(Equal(8192))
		Expect(create.Filter).To(Equal(xetrace.EventFilter{
			Types: []string{"SELECT", "DML"}, Tables: []string{"orders"},
		}))
		Expect(drain.Interval).To(Equal(2 * time.Second))
	})

	// Every unset knob must stay zero so xetrace applies its own documented
	// default. A value restated here would be a second place to keep in step.
	It("leaves unset options zero so xetrace defaults apply", func() {
		create, drain, err := sqlXEventOptions{SessionName: "acme_capture"}.captureOptions()
		Expect(err).ToNot(HaveOccurred())
		Expect(create.Events).To(BeEmpty())
		Expect(create.MinDurationMicros).To(BeZero())
		Expect(create.MaxEvents).To(BeZero())
		Expect(create.MaxMemoryKB).To(BeZero())
		Expect(drain.Interval).To(BeZero())
	})

	It("flattens and de-duplicates a comma-joined event list", func() {
		create, _, err := sqlXEventOptions{
			SessionName: "acme_capture",
			Events:      []string{"rpc_completed,sql_statement_completed", "rpc_completed"},
		}.captureOptions()
		Expect(err).ToNot(HaveOccurred())
		Expect(create.Events).To(Equal([]string{"rpc_completed", "sql_statement_completed"}))
	})

	DescribeTable("rejects a value the session could not honour",
		func(options sqlXEventOptions, message string) {
			_, _, err := options.captureOptions()
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(message))
		},
		Entry("unknown event", sqlXEventOptions{SessionName: "acme_capture", Events: []string{"lock_acquired"}}, `unsupported event "lock_acquired"`),
		Entry("unparseable minimum duration", sqlXEventOptions{SessionName: "acme_capture", MinDuration: "soon"}, `minDuration "soon"`),
		Entry("negative minimum duration", sqlXEventOptions{SessionName: "acme_capture", MinDuration: "-1s"}, `minDuration "-1s": must not be negative`),
		Entry("unparseable poll", sqlXEventOptions{SessionName: "acme_capture", Poll: "often"}, `poll "often"`),
		Entry("negative poll", sqlXEventOptions{SessionName: "acme_capture", Poll: "-1s"}, `poll "-1s": must not be negative`),
		Entry("negative event cap", sqlXEventOptions{SessionName: "acme_capture", MaxEvents: -1}, "maxEvents -1: must not be negative"),
		Entry("negative memory cap", sqlXEventOptions{SessionName: "acme_capture", MaxMemoryKB: -1}, "maxMemoryKb -1: must not be negative"),
		Entry("a named database and the whole instance",
			sqlXEventOptions{SessionName: "acme_capture", Database: "warehouse", AllDatabases: true}, "mutually exclusive"),
		Entry("no session name", sqlXEventOptions{}, "sessionName is required"),
		Entry("a blank session name", sqlXEventOptions{SessionName: "  "}, "sessionName is required"),
	)

	// The name reaches the server, where it must not collide with a capture the
	// same profile already started — and must still say whose it is.
	It("keeps the caller's name and makes it unique per session", func() {
		first, _, err := sqlXEventOptions{SessionName: "acme_capture"}.captureOptions()
		Expect(err).ToNot(HaveOccurred())
		second, _, err := sqlXEventOptions{SessionName: "acme_capture"}.captureOptions()
		Expect(err).ToNot(HaveOccurred())
		Expect(first.Name).To(HavePrefix("acme_capture_"))
		Expect(second.Name).To(HavePrefix("acme_capture_"))
		Expect(first.Name).ToNot(Equal(second.Name))
	})

	It("carries an instance-wide capture through as its own flag", func() {
		create, _, err := sqlXEventOptions{SessionName: "acme_capture", AllDatabases: true}.captureOptions()
		Expect(err).ToNot(HaveOccurred())
		Expect(create.AllDatabases).To(BeTrue())
		Expect(create.DatabaseName).To(BeEmpty())
	})
})

var _ = Describe("SQL XEvents option decoding", func() {
	It("reads every option the provider defines", func() {
		decoded, err := decodeXEventOptions(map[string]any{
			"database": "OIPA", "minDuration": "10ms", "maxEvents": float64(500),
			"events": []any{"rpc_completed"}, "types": []any{"SELECT"},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(decoded.Database).To(Equal("OIPA"))
		Expect(decoded.MinDuration).To(Equal("10ms"))
		Expect(decoded.MaxEvents).To(Equal(500))
		Expect(decoded.Events).To(Equal([]string{"rpc_completed"}))
		Expect(decoded.Types).To(Equal([]string{"SELECT"}))
	})

	// Silently ignoring it would start a capture that records every statement on
	// the instance while the author believes they asked for the slow ones.
	It("refuses an option it does not define rather than dropping it", func() {
		_, err := decodeXEventOptions(map[string]any{"minDurationn": "10ms"})
		Expect(err).To(MatchError(ContainSubstring("minDurationn")))
	})

	It("reads absent options as no options at all", func() {
		decoded, err := decodeXEventOptions(nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(decoded).To(Equal(sqlXEventOptions{}))
	})
})

var _ = Describe("SQL XEvents provider execution", func() {
	It("is registered under its catalog type", func() {
		provider, err := query.GetProvider(SQLXEventProviderType)
		Expect(err).ToNot(HaveOccurred())
		Expect(provider.Type()).To(Equal(SQLXEventProviderType))
	})

	It("streams, so a profile can declare it as a trace", func() {
		provider, err := query.GetProvider(SQLXEventProviderType)
		Expect(err).ToNot(HaveOccurred())
		_, streams := provider.(query.StreamProvider)
		Expect(streams).To(BeTrue())
	})

	// A single-shot read would report "no rows" for a capture that never ran,
	// which reads as an answer rather than the missing `trace:` block it is.
	It("refuses a single-shot read instead of returning an empty page", func() {
		rows, err := sqlXEventProvider{}.Execute(context.New(), query.ProviderRequest{})
		Expect(rows).To(BeNil())
		Expect(err).To(MatchError(ContainSubstring("has no single-shot form")))
	})
})
