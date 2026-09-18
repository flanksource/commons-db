package sessions

import (
	"context"
	"encoding/json"
	"time"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
)

// contractEpoch anchors the contract fixture an hour back, well inside every
// store's retention window, so window filters read as minute offsets.
func contractEpoch() time.Time { return time.Now().Add(-time.Hour).Truncate(time.Millisecond).UTC() }

func contractRecord(epoch time.Time, id, profile string, state query.SessionState, startedMin int, events int64, labels map[string]string) query.SessionRecord {
	started := epoch.Add(time.Duration(startedMin) * time.Minute)
	return query.SessionRecord{
		SessionStart: query.SessionStart{
			SchemaVersion: query.SessionSchemaVersion, ID: id, Profile: profile, Kind: query.KindCapture,
			Role: query.SessionRoleCapture, Labels: labels, Principal: "admin",
			Params: map[string]any{"class": "com.example.Listener"},
			Owner:  query.SessionOwner{Host: "pod-a", PID: 1, Boot: "boot-a"}, StartedAt: started,
		},
		SessionStatus: query.SessionStatus{State: state, EventCount: events, UpdatedAt: started, HeartbeatAt: started},
	}
}

// contractFixture mirrors the query package's filter fixture: b and c start at
// the same minute so a startedAt sort needs the id tie-breaker, and d has no
// labels at all.
func contractFixture(epoch time.Time) []query.SessionRecord {
	restarted := contractRecord(epoch, "f", "trace-capture/jvm_trace", query.SessionRunning, 5, 1, map[string]string{"target": "cycle", "origin": "web"})
	restarted.RestartOf = "c"
	restarted.Principal = "operator"
	return []query.SessionRecord{
		contractRecord(epoch, "c", "trace-capture/jvm_trace", query.SessionStopped, 2, 30, map[string]string{"target": "cycle", "origin": "web"}),
		contractRecord(epoch, "a", "trace-capture/jvm_trace", query.SessionRunning, 0, 10, map[string]string{"target": "oipa", "origin": "testplan"}),
		contractRecord(epoch, "b", "trace-capture/sql_xevent", query.SessionCompleted, 2, 20, map[string]string{"target": "cycle", "origin": "web"}),
		contractRecord(epoch, "d", "trace-capture/jvm_trace", query.SessionFailed, 3, 5, nil),
		restarted,
	}
}

func pageIDs(page query.SessionPage) []string {
	ids := make([]string, len(page.Items))
	for i, item := range page.Items {
		ids[i] = item.ID
	}
	return ids
}

// expectSameRecord compares records as their JSON, which is what every store
// persists and what the API serves.
func expectSameRecord(got, want query.SessionRecord) {
	gotJSON, err := json.Marshal(got)
	Expect(err).ToNot(HaveOccurred())
	wantJSON, err := json.Marshal(want)
	Expect(err).ToNot(HaveOccurred())
	Expect(gotJSON).To(MatchJSON(wantJSON))
}

// describeSessionStoreContract registers the specs every query.SessionStore
// must pass; newStore is called before each spec for an empty store.
func describeSessionStoreContract(newStore func() query.SessionStore) {
	var store query.SessionStore
	var epoch time.Time
	ctx := context.Background()

	ginkgo.BeforeEach(func() {
		store, epoch = newStore(), contractEpoch()
		for _, rec := range contractFixture(epoch) {
			Expect(store.Begin(ctx, rec)).To(Succeed())
		}
	})

	ginkgo.It("returns a begun record exactly as it was written", func() {
		want := contractFixture(epoch)[0]
		got, found, err := store.Get(ctx, want.ID)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		expectSameRecord(got, want)

		_, found, err = store.Get(ctx, "missing")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	ginkgo.It("refuses to begin a record twice", func() {
		Expect(store.Begin(ctx, contractFixture(epoch)[0])).To(MatchError(ContainSubstring("already exists")))
	})

	ginkgo.It("overwrites the status of a begun record and refuses one never begun", func() {
		rec := contractFixture(epoch)[1]
		stopped := epoch.Add(9 * time.Minute)
		rec.State, rec.StoppedAt, rec.StopReason, rec.EventCount = query.SessionStopped, &stopped, "stopped by admin", 99
		rec.Events = &query.EventsRef{Stream: "stream-a", Kind: "jvm_trace", From: 1, To: 99, Total: 99}
		rec.Summary = json.RawMessage(`{"calls":99}`)
		rec.Metadata = []query.SessionMetadata{{Name: "xe.statements", Label: "Started with", Language: "sql", Value: "CREATE EVENT SESSION [t] ON SERVER;"}}
		Expect(store.Update(ctx, rec.ID, rec.SessionStatus)).To(Succeed())

		got, _, err := store.Get(ctx, rec.ID)
		Expect(err).ToNot(HaveOccurred())
		expectSameRecord(got, rec)
		Expect(store.Update(ctx, "never-begun", rec.SessionStatus)).To(MatchError(ContainSubstring("never-begun")))
	})

	ginkgo.DescribeTable("lists records selected, authorized, sorted and paged",
		func(filter func(epoch time.Time) query.SessionFilter, wantIDs []string, wantTotal int) {
			page, err := store.List(ctx, filter(epoch))
			Expect(err).ToNot(HaveOccurred())
			Expect(pageIDs(page)).To(Equal(wantIDs))
			Expect(page.Total).To(Equal(wantTotal))
		},
		ginkgo.Entry("everything by startedAt ascending, id breaking the b/c tie",
			func(time.Time) query.SessionFilter { return query.SessionFilter{} }, []string{"a", "b", "c", "d", "f"}, 5),
		ginkgo.Entry("descending reverses both keys",
			func(time.Time) query.SessionFilter { return query.SessionFilter{Desc: true} }, []string{"f", "d", "c", "b", "a"}, 5),
		ginkgo.Entry("a profile suffix wildcard and a state exclusion",
			func(time.Time) query.SessionFilter {
				return query.SessionFilter{Profile: []string{"*jvm_trace"}, State: []string{"!failed"}}
			}, []string{"a", "c", "f"}, 3),
		ginkgo.Entry("states included case-insensitively",
			func(time.Time) query.SessionFilter {
				return query.SessionFilter{State: []string{"RUNNING", "stopped"}}
			}, []string{"a", "c", "f"}, 3),
		ginkgo.Entry("a label value, which a record without the label never matches",
			func(time.Time) query.SessionFilter {
				return query.SessionFilter{Labels: map[string][]string{"target": {"cycle"}}}
			}, []string{"b", "c", "f"}, 3),
		ginkgo.Entry("a label exclusion, which a record without the label matches",
			func(time.Time) query.SessionFilter {
				return query.SessionFilter{Labels: map[string][]string{"origin": {"!web"}}}
			}, []string{"a", "d"}, 2),
		ginkgo.Entry("principal and restartOf",
			func(time.Time) query.SessionFilter {
				return query.SessionFilter{Principal: []string{"oper*"}, RestartOf: []string{"c"}}
			}, []string{"f"}, 1),
		ginkgo.Entry("the startedAt window, inclusive at both ends",
			func(epoch time.Time) query.SessionFilter {
				return query.SessionFilter{From: epoch.Add(2 * time.Minute), To: epoch.Add(3 * time.Minute)}
			}, []string{"b", "c", "d"}, 3),
		ginkgo.Entry("Allow before the total, so refused profiles are never counted",
			func(time.Time) query.SessionFilter {
				return query.SessionFilter{Allow: func(profile string) bool { return profile == "trace-capture/sql_xevent" }}
			}, []string{"b"}, 1),
		ginkgo.Entry("eventCount descending with the page after the first",
			func(time.Time) query.SessionFilter {
				return query.SessionFilter{Sort: "eventCount", Desc: true, Limit: 2, Offset: 1}
			}, []string{"b", "a"}, 5),
		ginkgo.Entry("stoppedAt ascending puts the never-stopped first, by id",
			func(time.Time) query.SessionFilter { return query.SessionFilter{Sort: "stoppedAt", Limit: 2} },
			[]string{"a", "b"}, 5),
		ginkgo.Entry("an offset past the end is an empty page with the whole total",
			func(time.Time) query.SessionFilter { return query.SessionFilter{Offset: 10} }, []string{}, 5),
	)

	ginkgo.It("refuses a sort outside the allowlist", func() {
		_, err := store.List(ctx, query.SessionFilter{Sort: "error"})
		Expect(err).To(MatchError(ContainSubstring(`session sort "error" is not one of`)))
	})

	ginkgo.It("maps each id to the sessions restarted from it", func() {
		lineage, err := store.Lineage(ctx, []string{"c", "a"})
		Expect(err).ToNot(HaveOccurred())
		Expect(lineage).To(Equal(map[string][]string{"c": {"f"}}))
	})
}
