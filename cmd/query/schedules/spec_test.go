package schedules_test

import (
	"encoding/json"
	"time"

	"github.com/flanksource/clicky/task"
	"github.com/flanksource/commons-db/cmd/query/schedules"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// valid is the smallest schedule that passes validation, so each spec can break
// exactly one thing and see only that reported.
func valid() schedules.Schedule {
	return schedules.Schedule{
		Name:    "nightly",
		Cron:    "0 6 * * *",
		Enabled: true,
		Query:   &schedules.QuerySpec{Profile: "orders"},
	}
}

var _ = Describe("Schedule validation", func() {
	It("accepts a minimal query schedule", func() {
		Expect(valid().Validate()).To(Succeed())
	})

	It("requires a name", func() {
		schedule := valid()
		schedule.Name = ""
		Expect(schedule.Validate()).To(MatchError(ContainSubstring("name is required")))
	})

	It("requires something to run", func() {
		schedule := valid()
		schedule.Query = nil
		Expect(schedule.Validate()).To(MatchError(ContainSubstring("query or reconcile")))
	})

	// Both set is ambiguous rather than additive: there is one result, and two
	// sources for it means neither is the answer.
	It("refuses both a query and a reconcile", func() {
		schedule := valid()
		schedule.Reconcile = &schedules.ReconcileSpec{Profile: "orders"}
		Expect(schedule.Validate()).To(MatchError(ContainSubstring("mutually exclusive")))
	})

	It("requires the profile each mode reads from", func() {
		schedule := valid()
		schedule.Query = &schedules.QuerySpec{}
		Expect(schedule.Validate()).To(MatchError(ContainSubstring("query.profile")))

		reconciling := valid()
		reconciling.Query = nil
		reconciling.Reconcile = &schedules.ReconcileSpec{}
		Expect(reconciling.Validate()).To(MatchError(ContainSubstring("reconcile.profile")))
	})

	It("rejects a cron, timezone or policy it could never act on", func() {
		badCron := valid()
		badCron.Cron = "every night please"
		Expect(badCron.Validate()).To(MatchError(ContainSubstring("invalid cron")))

		badZone := valid()
		badZone.Timezone = "Mars/Olympus"
		Expect(badZone.Validate()).To(MatchError(ContainSubstring("unknown timezone")))

		badOverlap := valid()
		badOverlap.Overlap = "occasionally"
		Expect(badOverlap.Validate()).To(MatchError(ContainSubstring("overlap policy")))

		badCatchUp := valid()
		badCatchUp.CatchUp = "always"
		Expect(badCatchUp.Validate()).To(MatchError(ContainSubstring("catch-up policy")))
	})

	It("rejects a report format nothing can render", func() {
		schedule := valid()
		schedule.Report = &schedules.ReportSpec{Format: "powerpoint"}
		Expect(schedule.Validate()).To(MatchError(ContainSubstring("unsupported report format")))
	})

	It("accepts every format the export pipeline and facet between them serve", func() {
		for _, format := range []string{
			"csv", "json", "ndjson", "yaml", "markdown", "html", "excel", "pdf",
			"facet-html", "facet-pdf",
		} {
			schedule := valid()
			schedule.Report = &schedules.ReportSpec{Format: format}
			Expect(schedule.Validate()).To(Succeed(), format)
		}
	})

	It("requires a connection on every delivery target", func() {
		schedule := valid()
		schedule.Deliver = []schedules.DeliverySpec{{}}
		Expect(schedule.Validate()).To(MatchError(ContainSubstring("connection is required")))
	})

	// Attaching a report the schedule never produces would send an empty file
	// and look like it worked.
	It("refuses to attach a report the schedule does not produce", func() {
		schedule := valid()
		schedule.Deliver = []schedules.DeliverySpec{{Connection: "ops-email", Attach: true}}
		Expect(schedule.Validate()).To(MatchError(ContainSubstring("does not produce")))
	})

	It("accepts an attachment when a report is configured", func() {
		schedule := valid()
		schedule.Report = &schedules.ReportSpec{Format: "csv"}
		schedule.Deliver = []schedules.DeliverySpec{{Connection: "ops-email", Attach: true}}
		Expect(schedule.Validate()).To(Succeed())
	})
})

var _ = Describe("Schedule timing", func() {
	It("projects onto the task schedule the scheduler actually runs", func() {
		schedule := valid()
		schedule.Timezone = "Europe/Berlin"
		schedule.Overlap = string(task.OverlapQueue)
		schedule.CatchUp = string(task.CatchUpOnce)
		schedule.Timeout = schedules.Duration(90 * time.Second)
		schedule.Labels = map[string]string{"team": "ops"}

		timing := schedule.Timing()
		Expect(timing.Name).To(Equal("nightly"))
		Expect(timing.Kind).To(Equal(schedules.Kind))
		Expect(timing.Cron).To(Equal("0 6 * * *"))
		Expect(timing.Timezone).To(Equal("Europe/Berlin"))
		Expect(timing.Overlap).To(Equal(task.OverlapQueue))
		Expect(timing.CatchUp).To(Equal(task.CatchUpOnce))
		Expect(timing.Timeout).To(Equal(90 * time.Second))
		Expect(timing.Labels).To(HaveKeyWithValue("team", "ops"))
		Expect(timing.Enabled).To(BeTrue())
	})
})

// Durations are typed by people, so they round-trip as "30m" rather than as a
// nanosecond count nobody would write by hand.
var _ = Describe("Duration", func() {
	It("round-trips through JSON as a human string", func() {
		encoded, err := json.Marshal(schedules.Duration(90 * time.Second))
		Expect(err).ToNot(HaveOccurred())
		Expect(string(encoded)).To(Equal(`"1m30s"`))

		var decoded schedules.Duration
		Expect(json.Unmarshal([]byte(`"2h"`), &decoded)).To(Succeed())
		Expect(time.Duration(decoded)).To(Equal(2 * time.Hour))
	})

	It("reads an absent duration as zero rather than failing", func() {
		var decoded schedules.Duration
		Expect(json.Unmarshal([]byte(`""`), &decoded)).To(Succeed())
		Expect(decoded).To(BeZero())
	})

	It("rejects a duration it cannot parse", func() {
		var decoded schedules.Duration
		Expect(json.Unmarshal([]byte(`"soon"`), &decoded)).To(MatchError(ContainSubstring("invalid duration")))
	})
})
