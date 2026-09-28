package xetrace

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/flanksource/commons-db/query"
)

// metered builds a top-level event with the six captured metrics set.
func metered(name string, d, cpu time.Duration, logical, physical, writes, rows int64) Event {
	return Event{
		Name:          name,
		Duration:      d,
		CPUTime:       cpu,
		LogicalReads:  logical,
		PhysicalReads: physical,
		Writes:        writes,
		RowCount:      rows,
	}
}

// meteredAct is metered plus a causality activity id, so the event takes part
// in parent/inner-statement attribution.
func meteredAct(name, activityID string, seq int, d, cpu time.Duration, logical int64) Event {
	e := metered(name, d, cpu, logical, 0, 0, 0)
	e.ActivityID = activityID
	e.ActivitySeq = seq
	return e
}

var _ = Describe("Trace summary", func() {
	Describe("a flat capture with no causality", func() {
		// Two independent batches, so every figure is a plain sum.
		events := []Event{
			metered(EventSQLBatchCompleted, 30*time.Millisecond, 20*time.Millisecond, 100, 4, 2, 7),
			metered(EventSQLStatementCompleted, 70*time.Millisecond, 40*time.Millisecond, 300, 6, 1, 3),
		}

		It("sums every metric across the events", func() {
			Expect(Summarize(events)).To(MatchFields(IgnoreExtras, Fields{
				"Events":        Equal(2),
				"Nested":        Equal(0),
				"Duration":      Equal(100 * time.Millisecond),
				"CPUTime":       Equal(60 * time.Millisecond),
				"LogicalReads":  Equal(int64(400)),
				"PhysicalReads": Equal(int64(10)),
				"Writes":        Equal(int64(3)),
				"RowCount":      Equal(int64(10)),
				"MaxDuration":   Equal(70 * time.Millisecond),
				"AvgDuration":   Equal(50 * time.Millisecond),
			}))
		})

		It("derives read bytes from the logical page count alone", func() {
			// A page read from disk is also counted as a logical read, so adding
			// the 10 physical reads would count those pages twice.
			Expect(Summarize(events).ReadBytes).To(Equal(int64(400 * 8192)))
		})

		It("reports CPU as a fraction of elapsed time", func() {
			Expect(Summarize(events).CPURatio()).To(BeNumerically("~", 0.6, 0.001))
		})
	})

	Describe("a procedure whose inner statements were also captured", func() {
		// XE raises inner statements BEFORE the call that ran them, and the
		// rpc's own figures already include their time and IO.
		events := []Event{
			meteredAct(EventSPStatementCompleted, "A", 1, 20*time.Millisecond, 15*time.Millisecond, 200),
			meteredAct(EventSPStatementCompleted, "A", 2, 30*time.Millisecond, 25*time.Millisecond, 300),
			meteredAct(EventRPCCompleted, "A", 3, 60*time.Millisecond, 45*time.Millisecond, 500),
		}

		It("counts the inner statements without double-counting their cost", func() {
			Expect(Summarize(events)).To(MatchFields(IgnoreExtras, Fields{
				"Events":       Equal(3),
				"Nested":       Equal(2),
				"Duration":     Equal(60 * time.Millisecond),
				"CPUTime":      Equal(45 * time.Millisecond),
				"LogicalReads": Equal(int64(500)),
			}))
		})

		It("gives the same totals once Nest has folded them into Children", func() {
			// The CLI nests before rendering; both shapes must agree.
			Expect(Summarize(Nest(events))).To(Equal(Summarize(events)))
		})

		It("agrees whether folded event-at-a-time or as a slice", func() {
			// Drain emits parents ahead of their statements, so streaming Add
			// must reach the same verdict as AddAll's pre-scan.
			streamed := NewAccumulator()
			for _, e := range []Event{events[2], events[0], events[1]} {
				streamed.Add(e)
			}
			Expect(streamed.Result()).To(Equal(Summarize(events)))
		})
	})

	Describe("the part of a capture a statement pattern selects", func() {
		// The shape one ORM query builder emits, as the driver sends it: its own
		// line breaks and indentation, which no fixture author will retype.
		findNext := metered(EventRPCCompleted, 6*time.Millisecond, 5*time.Millisecond, 1500, 0, 0, 36)
		findNext.SQL = "SELECT AsActivity.ActivityGUID\n        FROM AsActivity \n         JOIN AsClientActivity ON AsActivity.ActivityGuid = AsClientActivity.ActivityGuid"
		marked := metered(EventRPCCompleted, 2*time.Millisecond, 2*time.Millisecond, 200, 0, 0, 1)
		marked.SQL = "/* query-variant=top1 */ SELECT TOP 1 AsActivity.ActivityGUID FROM AsActivity JOIN AsClientActivity ON AsActivity.ActivityGuid = AsClientActivity.ActivityGuid"
		lookup := metered(EventRPCCompleted, time.Millisecond, time.Millisecond, 5, 0, 0, 1)
		lookup.SQL = "SELECT XMLDATA FROM AsActivity WHERE (ACTIVITYGUID = @P0)"

		It("sums only the events whose text contains the pattern, ignoring case and line breaks", func() {
			Expect(SummarizeMatching([]Event{findNext, lookup, marked}, "join asclientactivity   ON asactivity.ActivityGuid")).
				To(MatchFields(IgnoreExtras, Fields{
					"Events":       Equal(2),
					"LogicalReads": Equal(int64(1700)),
					"RowCount":     Equal(int64(37)),
					"ReadBytes":    Equal(int64(1700 * 8192)),
				}))
		})

		It("selects the calls carrying a marker comment", func() {
			Expect(SummarizeMatching([]Event{findNext, lookup, marked}, "query-variant=top1").Events).To(Equal(1))
		})

		// A re-run of a prepared statement arrives as `exec sp_execute N`; only its
		// resolved SQL says what it ran, so that is the text a pattern must see.
		It("matches a prepared re-run by the statement it resolved to", func() {
			rerun := findNext
			rerun.Statement = "exec sp_execute 5,N'4217A05E-4869-4B3F-841B-EC5F5111EE2D'"

			Expect(SummarizeMatching([]Event{rerun}, "JOIN AsClientActivity").Events).To(Equal(1))
		})

		It("falls back to the raw statement when nothing was resolved from it", func() {
			raw := metered(EventSQLBatchCompleted, time.Millisecond, time.Millisecond, 9, 0, 0, 0)
			raw.Statement = "UPDATE AsActivity SET StatusCode = '01'"

			Expect(SummarizeMatching([]Event{raw}, "update asactivity").LogicalReads).To(Equal(int64(9)))
		})

		// The pattern selects calls; a matching call's inner statements are its
		// own cost, counted but never summed a second time.
		It("counts a matching call's inner statements without double-counting them", func() {
			parent := meteredAct(EventRPCCompleted, "A", 3, 60*time.Millisecond, 45*time.Millisecond, 500)
			parent.SQL = "EXEC usp_LockEntities @owner"
			parent.Children = []Event{meteredAct(EventSPStatementCompleted, "A", 1, 20*time.Millisecond, 15*time.Millisecond, 200)}

			Expect(SummarizeMatching([]Event{parent, lookup}, "usp_LockEntities")).To(MatchFields(IgnoreExtras, Fields{
				"Events":       Equal(2),
				"Nested":       Equal(1),
				"LogicalReads": Equal(int64(500)),
			}))
		})
	})

	Describe("an inner statement whose parent never arrived", func() {
		// --min-duration filtered the rpc out, or it completed after the
		// window closed. Its cost is real and nothing else reports it.
		events := []Event{
			meteredAct(EventSPStatementCompleted, "orphan", 1, 25*time.Millisecond, 10*time.Millisecond, 90),
		}

		It("counts it as top-level rather than dropping its cost", func() {
			Expect(Summarize(events)).To(MatchFields(IgnoreExtras, Fields{
				"Events":       Equal(1),
				"Nested":       Equal(0),
				"Duration":     Equal(25 * time.Millisecond),
				"LogicalReads": Equal(int64(90)),
			}))
		})
	})

	Describe("derived figures", func() {
		It("takes the 95th percentile by nearest rank", func() {
			// 20 events of 1..20ms: ceil(0.95×20)=19th value = 19ms.
			var events []Event
			for i := 1; i <= 20; i++ {
				events = append(events, metered(EventSQLStatementCompleted, time.Duration(i)*time.Millisecond, 0, 0, 0, 0, 0))
			}
			Expect(Summarize(events).P95Duration).To(Equal(19 * time.Millisecond))
		})

		It("spans wall-clock first event to last, not the sum of durations", func() {
			base := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
			first := metered(EventSQLStatementCompleted, time.Second, 0, 0, 0, 0, 0)
			first.Timestamp = base
			last := metered(EventSQLStatementCompleted, time.Second, 0, 0, 0, 0, 0)
			last.Timestamp = base.Add(90 * time.Second)

			// Concurrent sessions overlap, so 2s of duration spans 90s of clock.
			Expect(Summarize([]Event{last, first}).Window).To(Equal(90 * time.Second))
		})

		It("counts error events", func() {
			bad := metered(EventErrorReported, 0, 0, 0, 0, 0, 0)
			bad.ErrorNumber = 1205
			Expect(Summarize([]Event{bad}).Errors).To(Equal(1))
		})
	})

	Describe("a capture that did not see everything", func() {
		events := []Event{metered(EventSQLBatchCompleted, 30*time.Millisecond, 20*time.Millisecond, 100, 4, 2, 7)}

		incomplete := func() Summary {
			a := NewAccumulator()
			a.AddAll(events)
			a.AddLost(7)
			a.AddLost(3)
			a.AddUnresolved()
			return a.Result()
		}

		It("counts what it lost and what it could not attribute, apart from the metrics", func() {
			Expect(incomplete()).To(MatchFields(IgnoreExtras, Fields{
				"Lost":         Equal(int64(10)),
				"Unresolved":   Equal(int64(1)),
				"Events":       Equal(1),
				"LogicalReads": Equal(int64(100)),
			}))
		})

		It("says so on the cost line", func() {
			out := incomplete().Pretty().String()
			Expect(out).To(ContainSubstring("10 lost"))
			Expect(out).To(ContainSubstring("1 unresolved"))
		})

		It("stays quiet about a complete capture", func() {
			out := Summarize(events).Pretty().String()
			Expect(out).NotTo(ContainSubstring("lost"))
			Expect(out).NotTo(ContainSubstring("unresolved"))
		})

		It("carries both counts on the wire", func() {
			raw, err := json.Marshal(incomplete())
			Expect(err).ToNot(HaveOccurred())
			Expect(string(raw)).To(ContainSubstring(`"lost":10`))
			Expect(string(raw)).To(ContainSubstring(`"unresolved":1`))
		})
	})

	Describe("a capture holding events that report no duration", func() {
		// An error and a schema change are counted, but they took no measured
		// time: sampled as 0 they would drag the average and p95 down.
		events := []Event{
			metered(EventSQLBatchCompleted, 30*time.Millisecond, 20*time.Millisecond, 100, 4, 2, 7),
			metered(EventSQLStatementCompleted, 70*time.Millisecond, 40*time.Millisecond, 300, 6, 1, 3),
			{Name: EventErrorReported, ErrorNumber: 1205},
			{Name: EventObjectCreated, ObjectName: "AsAudit", ObjectType: "USRTAB"},
		}

		It("counts them, but samples only the timed events for the average and p95", func() {
			Expect(Summarize(events)).To(MatchFields(IgnoreExtras, Fields{
				"Events":      Equal(4),
				"Errors":      Equal(1),
				"Duration":    Equal(100 * time.Millisecond),
				"AvgDuration": Equal(50 * time.Millisecond),
				"P95Duration": Equal(70 * time.Millisecond),
			}))
		})
	})

	Describe("an empty capture", func() {
		It("reports zeroes rather than dividing by zero", func() {
			Expect(Summarize(nil)).To(Equal(Summary{}))
			Expect(Summary{}.CPURatio()).To(BeZero())
		})
	})

	Describe("Pretty", func() {
		events := []Event{metered(EventSQLBatchCompleted, 30*time.Millisecond, 20*time.Millisecond, 100, 4, 2, 7)}

		It("renders the cost line", func() {
			out := Summarize(events).Pretty().String()
			Expect(out).To(ContainSubstring("elapsed"))
			Expect(out).To(ContainSubstring("cpu"))
			Expect(out).To(ContainSubstring("reads"))
			Expect(out).To(ContainSubstring("writes"))
			Expect(out).To(ContainSubstring("rows"))
		})

		It("quotes logical reads and shows physical reads beside them, not added in", func() {
			out := Summarize(events).Pretty().String()
			Expect(out).To(ContainSubstring("100 reads"))
			Expect(out).To(ContainSubstring("4 physical"))
		})

		It("is carried into the trace result the CLI renders", func() {
			summary := Summarize(events)
			out := TraceResult{Database: "warehouse", Events: events, Summary: &summary}.Pretty().String()
			Expect(out).To(ContainSubstring("1 events"))
			Expect(out).To(ContainSubstring("elapsed"))
		})

		It("is omitted rather than rendered empty when a producer set none", func() {
			out := TraceResult{Database: "warehouse", Events: events}.Pretty().String()
			Expect(out).To(ContainSubstring("1 events"))
			Expect(out).NotTo(ContainSubstring("elapsed"))
		})

		It("renders the bounded preview while pointing at the persisted result stream", func() {
			preview := append([]Event{}, events...)
			preview[0].SQL = "SELECT preview"
			out := TraceResult{
				Database: "warehouse", Preview: preview,
				Ref: &query.EventsRef{Stream: "trace-42", Kind: "sql_xevent", Total: 42, From: 1, To: 42},
			}.Pretty().String()

			Expect(out).To(ContainSubstring("42 events"))
			Expect(out).To(ContainSubstring("SELECT preview"))
			Expect(out).To(ContainSubstring("page stream trace-42 for all 42"))
		})
	})

	Describe("the serialized wire contract", func() {
		// The test-runner stores a sql_xevent span's TraceResult as JSON and the
		// web UI reads it back much later, so a missing key is only noticed when
		// the strip silently fails to render on a stored run.
		summary := Summarize([]Event{
			metered(EventSQLBatchCompleted, 30*time.Millisecond, 20*time.Millisecond, 100, 4, 2, 7),
		})

		It("carries summary alongside events", func() {
			raw, err := json.Marshal(TraceResult{Database: "warehouse", Events: []Event{{Name: EventRPCCompleted}}, Summary: &summary})
			Expect(err).ToNot(HaveOccurred())

			var decoded map[string]any
			Expect(json.Unmarshal(raw, &decoded)).To(Succeed())
			Expect(decoded).To(HaveKey("summary"))
			Expect(decoded).To(HaveKey("events"))
		})

		It("names every field the TypeScript SqlTraceSummary mirrors", func() {
			// Keep in sync with www/src/types/sql-server.ts. A rename here that is
			// not mirrored there reads as a missing figure, not as an error.
			raw, err := json.Marshal(summary)
			Expect(err).ToNot(HaveOccurred())

			var decoded map[string]any
			Expect(json.Unmarshal(raw, &decoded)).To(Succeed())
			for _, key := range []string{
				"events", "window", "duration", "avg_duration", "p95_duration", "max_duration",
				"cpu_time", "logical_reads", "physical_reads", "read_bytes", "writes", "row_count",
			} {
				Expect(decoded).To(HaveKey(key))
			}
		})

		It("emits durations as nanoseconds, which is what the UI formats", func() {
			raw, err := json.Marshal(summary)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(raw)).To(ContainSubstring(`"duration":30000000`))
		})
	})
})
