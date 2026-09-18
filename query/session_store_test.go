package query_test

import (
	"time"

	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// filterEpoch anchors the fixture's start times so windows read as offsets.
var filterEpoch = time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

func filterRecord(id, profile string, state query.SessionState, startedMin int, events int64, labels map[string]string) query.SessionRecord {
	return query.SessionRecord{
		SessionStart: query.SessionStart{
			ID: id, Profile: profile, Kind: query.KindCapture, Role: query.SessionRoleCapture,
			Labels: labels, Principal: "admin", StartedAt: filterEpoch.Add(time.Duration(startedMin) * time.Minute),
		},
		SessionStatus: query.SessionStatus{State: state, EventCount: events},
	}
}

// filterFixture: two JVM traces on cycle and oipa, one SQL trace, one view, and
// a JVM trace with no target label. b and c start at the same minute so a sort
// by startedAt needs the id tie-breaker.
func filterFixture() []query.SessionRecord {
	view := filterRecord("e", "trace-results/jvm_trace", query.SessionRunning, 4, 0, nil)
	view.Role = query.SessionRoleView
	return []query.SessionRecord{
		filterRecord("c", "trace-capture/jvm_trace", query.SessionStopped, 2, 30, map[string]string{"target": "cycle", "origin": "web"}),
		filterRecord("a", "trace-capture/jvm_trace", query.SessionRunning, 0, 10, map[string]string{"target": "oipa", "origin": "testplan"}),
		filterRecord("b", "trace-capture/sql_xevent", query.SessionCompleted, 2, 20, map[string]string{"target": "cycle", "origin": "web"}),
		filterRecord("d", "trace-capture/jvm_trace", query.SessionFailed, 3, 5, nil),
		view,
	}
}

func pageIDs(page query.SessionPage) []string {
	ids := make([]string, len(page.Items))
	for i, item := range page.Items {
		ids[i] = item.ID
	}
	return ids
}

var _ = Describe("ApplySessionFilter", func() {
	DescribeTable("selects, authorizes, sorts and pages records",
		func(filter query.SessionFilter, wantIDs []string, wantTotal int) {
			page, err := query.ApplySessionFilter(filterFixture(), filter)
			Expect(err).ToNot(HaveOccurred())
			Expect(pageIDs(page)).To(Equal(wantIDs))
			Expect(page.Total).To(Equal(wantTotal))
		},
		Entry("sorts by startedAt ascending with the id breaking the b/c tie", query.SessionFilter{},
			[]string{"a", "b", "c", "d", "e"}, 5),
		Entry("reverses both keys when descending", query.SessionFilter{Desc: true},
			[]string{"e", "d", "c", "b", "a"}, 5),
		Entry("matches a profile wildcard", query.SessionFilter{Profile: []string{"trace-capture/*"}},
			[]string{"a", "b", "c", "d"}, 4),
		Entry("excludes with !", query.SessionFilter{Profile: []string{"!*sql_xevent"}, Role: []string{"capture"}},
			[]string{"a", "c", "d"}, 3),
		Entry("matches any of several states", query.SessionFilter{State: []string{"running", "stopped"}},
			[]string{"a", "c", "e"}, 3),
		Entry("selects ids", query.SessionFilter{IDs: []string{"d", "b"}},
			[]string{"b", "d"}, 2),
		Entry("filters by a label value", query.SessionFilter{Labels: map[string][]string{"target": {"cycle"}}},
			[]string{"b", "c"}, 2),
		Entry("does not match a positive label pattern on a record without the label",
			query.SessionFilter{Labels: map[string][]string{"target": {"*"}}},
			[]string{"a", "b", "c"}, 3),
		Entry("matches a label exclusion on a record without the label",
			query.SessionFilter{Labels: map[string][]string{"target": {"!cycle"}}},
			[]string{"a", "d", "e"}, 3),
		Entry("bounds startedAt inclusively on both sides",
			query.SessionFilter{From: filterEpoch.Add(2 * time.Minute), To: filterEpoch.Add(3 * time.Minute)},
			[]string{"b", "c", "d"}, 3),
		Entry("sorts by eventCount descending", query.SessionFilter{Sort: "eventCount", Desc: true, Role: []string{"capture"}},
			[]string{"c", "b", "a", "d"}, 4),
		Entry("sorts by state with the id tie-breaker", query.SessionFilter{Sort: "state", State: []string{"running", "completed"}},
			[]string{"b", "a", "e"}, 3),
		Entry("pages after totalling every match", query.SessionFilter{Limit: 2, Offset: 1},
			[]string{"b", "c"}, 5),
		Entry("returns an empty page past the end", query.SessionFilter{Limit: 2, Offset: 10},
			[]string{}, 5),
		Entry("authorizes before totals and paging",
			query.SessionFilter{Limit: 1, Allow: func(profile string) bool { return profile != "trace-capture/jvm_trace" }},
			[]string{"b"}, 2),
	)

	It("asks Allow once per record the field filters kept, and totals only what it allows", func() {
		calls := map[string]int{}
		page, err := query.ApplySessionFilter(filterFixture(), query.SessionFilter{
			State: []string{"!failed"},
			Allow: func(profile string) bool { calls[profile]++; return profile == "trace-capture/sql_xevent" },
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(page.Total).To(Equal(1))
		Expect(calls).To(Equal(map[string]int{"trace-capture/jvm_trace": 2, "trace-capture/sql_xevent": 1, "trace-results/jvm_trace": 1}))
	})

	DescribeTable("rejects an invalid filter",
		func(filter query.SessionFilter, message string) {
			_, err := query.ApplySessionFilter(filterFixture(), filter)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("sort outside the allowlist", query.SessionFilter{Sort: "params"}, `session sort "params"`),
		Entry("negative limit", query.SessionFilter{Limit: -1}, "must not be negative"),
		Entry("window ending before it starts", query.SessionFilter{From: filterEpoch, To: filterEpoch.Add(-time.Minute)}, "is before from"),
	)
})
