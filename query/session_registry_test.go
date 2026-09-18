package query_test

import (
	stdcontext "context"
	"time"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// sweepRecord is an active record owned by owner whose heartbeat is age old.
func sweepRecord(id string, owner query.SessionOwner, state query.SessionState, age time.Duration) query.SessionRecord {
	now := time.Now()
	return query.SessionRecord{
		SessionStart: query.SessionStart{
			ID: id, Profile: jvmTraceProfile, Kind: query.KindCapture, Role: query.SessionRoleCapture,
			Owner: owner, StartedAt: now.Add(-time.Hour),
		},
		SessionStatus: query.SessionStatus{State: state, HeartbeatAt: now.Add(-age), UpdatedAt: now.Add(-age)},
	}
}

var _ = Describe("SessionRegistry.Restart", func() {
	var store *fakeSessionStore
	var restarted []query.TrackOptions
	var reg *query.SessionRegistry

	BeforeEach(func() {
		store, restarted = newFakeSessionStore(), nil
		reg = query.NewSessionRegistry(query.RegistryOptions{
			Store: store,
			Restarters: map[string]query.RestartFunc{
				"trace-capture/": func(ctx stdcontext.Context, _ query.SessionRecord, opts query.TrackOptions) (*query.Session, error) {
					restarted = append(restarted, opts)
					return reg.Track(ctx, opts)
				},
			},
		})
	})

	It("starts a new session from an ended one without writing the ended record", func() {
		stopAt := time.Now().Add(10 * time.Minute)
		opts := jvmTrackOptions()
		opts.StopAt = &stopAt
		previous := track(reg, stdcontext.Background(), opts)

		_, err := reg.Restart(stdcontext.Background(), previous.ID(), query.RestartOverrides{})
		Expect(err).To(MatchError(ContainSubstring("only an ended session restarts")))
		Expect(restarted).To(BeEmpty())

		previous.Finish(query.FinishUpdate{})
		writesBefore := store.writesFor(previous.ID())
		next, err := reg.Restart(stdcontext.Background(), previous.ID(), query.RestartOverrides{
			Params: map[string]any{"durationMs": 60000.0}, Principal: "operator",
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(next.Finish, query.FinishUpdate{})

		Expect(next.ID()).ToNot(Equal(previous.ID()))
		Expect(store.writesFor(previous.ID())).To(Equal(writesBefore), "the ended record is never written")
		Expect(restarted).To(HaveLen(1))
		Expect(restarted[0]).To(And(
			HaveField("Profile", jvmTraceProfile),
			HaveField("RestartOf", previous.ID()),
			HaveField("Principal", "operator"),
			HaveField("Labels", jvmTrackOptions().Labels),
			HaveField("Params", map[string]any{"class": "com.example.Listener", "durationMs": 60000.0}),
		))
		Expect(time.Until(*restarted[0].StopAt)).To(BeNumerically("~", time.Minute, 5*time.Second), "the replayed params' durationMs")
		begun, _ := store.record(next.ID())
		Expect(begun.RestartOf).To(Equal(previous.ID()))
	})

	It("replays the duration first requested, not the run an extension lengthened", func() {
		const requested = 2 * time.Minute
		stopAt := time.Now().Add(requested)
		opts := jvmTrackOptions()
		opts.Params = map[string]any{"class": "com.example.Listener", "durationMs": float64(requested.Milliseconds())}
		opts.StopAt = &stopAt
		previous := track(reg, stdcontext.Background(), opts)
		_, err := reg.Extend(previous.ID(), time.Now().Add(12*time.Minute))
		Expect(err).ToNot(HaveOccurred())
		previous.Finish(query.FinishUpdate{})

		next, err := reg.Restart(stdcontext.Background(), previous.ID(), query.RestartOverrides{})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(next.Finish, query.FinishUpdate{})
		Expect(time.Until(*restarted[0].StopAt)).To(BeNumerically("~", requested, 5*time.Second))

		next.Finish(query.FinishUpdate{})
		_, err = reg.Restart(stdcontext.Background(), next.ID(), query.RestartOverrides{Duration: 5 * time.Minute})
		Expect(err).ToNot(HaveOccurred())
		Expect(restarted[1].Params).To(HaveKeyWithValue("durationMs", 300000.0), "an override is what the new session requested")
		Expect(time.Until(*restarted[1].StopAt)).To(BeNumerically("~", 5*time.Minute, 5*time.Second))
	})

	It("refuses to replay a durationMs that is not a positive number", func() {
		rec := sweepRecord("odd", reg.Owner(), query.SessionStopped, time.Hour)
		rec.Params = map[string]any{"durationMs": "soon"}
		store.seed(rec)
		_, err := reg.Restart(stdcontext.Background(), "odd", query.RestartOverrides{})
		Expect(err).To(MatchError(ContainSubstring("params.durationMs")))
		Expect(restarted).To(BeEmpty())
	})

	It("stops a session the restarter started without naming the ended one", func() {
		var stray *query.Session
		reg = query.NewSessionRegistry(query.RegistryOptions{
			Store: store,
			Restarters: map[string]query.RestartFunc{
				"trace-capture/": func(ctx stdcontext.Context, _ query.SessionRecord, opts query.TrackOptions) (*query.Session, error) {
					opts.RestartOf = ""
					session, err := reg.Track(ctx, opts)
					if err == nil {
						stray = session
						finishOnStop(session)
					}
					return session, err
				},
			},
		})
		store.seed(sweepRecord("gone", reg.Owner(), query.SessionStopped, time.Hour))

		_, err := reg.Restart(stdcontext.Background(), "gone", query.RestartOverrides{})
		Expect(err).To(MatchError(ContainSubstring(`with restartOf ""`)))
		Expect(stray).ToNot(BeNil())
		waitDone(stray)
		Expect(stray.Snapshot()).To(And(
			HaveField("State", query.SessionStopped), HaveField("StopReason", ContainSubstring("restart of gone")),
		))
	})

	It("restarts a record only the store still holds", func() {
		store.seed(sweepRecord("gone", reg.Owner(), query.SessionStopped, time.Hour))
		next, err := reg.Restart(stdcontext.Background(), "gone", query.RestartOverrides{})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(next.Finish, query.FinishUpdate{})
		Expect(next.Snapshot().RestartOf).To(Equal("gone"))
	})

	It("refuses a profile without a restarter", func() {
		rec := sweepRecord("sql", reg.Owner(), query.SessionStopped, time.Hour)
		rec.Profile = "connection/trace"
		store.seed(rec)
		_, err := reg.Restart(stdcontext.Background(), "sql", query.RestartOverrides{})
		Expect(err).To(MatchError(ContainSubstring(`profile "connection/trace" has no restarter`)))
		Expect(reg.Restartable(rec)).To(BeFalse())
	})

	It("lets RegistryOptions.Restartable refuse a record a restarter matches", func() {
		refused := sweepRecord("refused", reg.Owner(), query.SessionStopped, time.Hour)
		refused.Labels = map[string]string{"origin": "testplan"}
		allowed := sweepRecord("allowed", reg.Owner(), query.SessionStopped, time.Hour)
		reg = query.NewSessionRegistry(query.RegistryOptions{
			Store: store,
			Restarters: map[string]query.RestartFunc{
				"trace-capture/": func(ctx stdcontext.Context, _ query.SessionRecord, opts query.TrackOptions) (*query.Session, error) {
					return reg.Track(ctx, opts)
				},
			},
			Restartable: func(rec query.SessionRecord) bool { return rec.Labels["origin"] != "testplan" },
		})
		store.seed(refused)

		Expect(reg.Restartable(allowed)).To(BeTrue())
		Expect(reg.Restartable(refused)).To(BeFalse())
		_, err := reg.Restart(stdcontext.Background(), "refused", query.RestartOverrides{})
		Expect(err).To(MatchError(query.ErrNotRestartable))
	})
})

var _ = Describe("SessionRegistry.Sweep", func() {
	It("interrupts only active records with a stale heartbeat, naming a restart of this host's owner", func() {
		store := newFakeSessionStore()
		reg := query.NewSessionRegistry(query.RegistryOptions{Store: store, StaleAfter: time.Minute})
		self := reg.Owner()
		otherHost := query.SessionOwner{Host: "other-pod", PID: 7, Boot: "b-other"}
		previousBoot := query.SessionOwner{Host: self.Host, PID: 9, Boot: "b-previous"}

		live := track(reg, stdcontext.Background(), jvmTrackOptions())
		DeferCleanup(live.Finish, query.FinishUpdate{})
		store.seed(sweepRecord("stale", otherHost, query.SessionRunning, 10*time.Minute))
		store.seed(sweepRecord("restarted-owner", previousBoot, query.SessionStopping, 10*time.Minute))
		store.seed(sweepRecord("sibling-process", previousBoot, query.SessionRunning, time.Second))
		store.seed(sweepRecord("fresh", otherHost, query.SessionRunning, time.Second))
		store.seed(sweepRecord("ended", otherHost, query.SessionCompleted, time.Hour))

		Expect(reg.Sweep(stdcontext.Background())).To(Succeed())

		Expect(store.status("stale")).To(And(
			HaveField("State", query.SessionInterrupted), HaveField("StopReason", query.StopReasonHeartbeatLost),
		))
		Expect(store.status("stale").StoppedAt).ToNot(BeNil())
		Expect(store.status("restarted-owner")).To(And(
			HaveField("State", query.SessionInterrupted), HaveField("StopReason", query.StopReasonOwnerRestart),
		))
		Expect(store.writesFor("sibling-process")).To(BeEmpty(),
			"a fresh heartbeat from another boot on this host is a healthy sibling process, e.g. a CLI capture beside serve")
		Expect(store.writesFor("fresh")).To(BeEmpty())
		Expect(store.writesFor("ended")).To(BeEmpty())
		Expect(store.writesFor(live.ID())).To(BeEmpty())
	})

	It("re-issues the final status of a session that ended here while its store copy is still active", func() {
		store := newFakeSessionStore()
		reg := query.NewSessionRegistry(query.RegistryOptions{Store: store})
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		Expect(session.Running(query.RunningUpdate{Handle: "probe"})).To(Succeed())
		store.failUpdates = 1
		session.Finish(query.FinishUpdate{})
		Expect(store.status(session.ID()).State).To(Equal(query.SessionRunning), "the terminal write was refused")

		Expect(reg.Sweep(stdcontext.Background())).To(Succeed())
		Expect(store.status(session.ID())).To(And(
			HaveField("State", query.SessionCompleted), HaveField("Handle", "probe"),
		))
	})

	It("refuses to sweep without a store", func() {
		reg := query.NewSessionRegistry(query.RegistryOptions{})
		Expect(reg.Sweep(stdcontext.Background())).To(MatchError(ContainSubstring("no session store")))
	})
})

var _ = Describe("SessionRegistry heartbeats and views", func() {
	It("writes heartbeats for active captures", func() {
		store := newFakeSessionStore()
		reg := query.NewSessionRegistry(query.RegistryOptions{Store: store, HeartbeatEvery: 20 * time.Millisecond})
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		DeferCleanup(session.Finish, query.FinishUpdate{})
		startedAt := session.Snapshot().StartedAt

		Eventually(func() time.Time { return store.status(session.ID()).HeartbeatAt }, "2s").
			Should(BeTemporally(">", startedAt.Add(20*time.Millisecond)))
	})

	It("persists a capture stream and its events but never a view", func() {
		store, sink := newFakeSessionStore(), &fakeEventSink{}
		reg := query.NewSessionRegistry(query.RegistryOptions{Store: store, Events: sink})
		query.RegisterProvider(&fakeStreamProvider{typ: "stream-roles", rows: []query.Row{{"n": 1}, {"n": 2}}})

		view, err := query.ExecuteStream(context.New(), reg, query.Follow(traceProfile("stream-roles")))
		Expect(err).ToNot(HaveOccurred())
		capture, err := query.ExecuteStream(context.New(), reg, traceProfile("stream-roles"))
		Expect(err).ToNot(HaveOccurred())
		waitDone(view)
		waitDone(capture)

		Expect(view.Snapshot().Role).To(Equal(query.SessionRoleView))
		_, persisted := store.record(view.ID())
		Expect(persisted).To(BeFalse())
		Expect(sink.appended(view.ID())).To(BeEmpty())

		Expect(store.recordCount()).To(Equal(1))
		Expect(store.status(capture.ID())).To(And(
			HaveField("State", query.SessionCompleted), HaveField("EventCount", int64(2)),
		))
		Expect(sink.appended(capture.ID())).To(Equal([]int64{1, 2}))
	})

})

var _ = Describe("SessionRegistry.StopAll", func() {
	It("returns only once every session's final status is written", func() {
		store := newFakeSessionStore()
		reg := query.NewSessionRegistry(query.RegistryOptions{Store: store})
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		session.OnStop(func(string) {
			time.Sleep(80 * time.Millisecond) // the host's flush
			session.Finish(query.FinishUpdate{})
		})

		Expect(reg.StopAll(stdcontext.Background())).To(Succeed())
		Expect(store.status(session.ID())).To(And(
			HaveField("State", query.SessionStopped), HaveField("StopReason", query.StopReasonShutdown),
		))
	})

	It("fails when a session outlasts the shutdown context", func() {
		reg := query.NewSessionRegistry(query.RegistryOptions{})
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		session.OnStop(func(string) {})
		DeferCleanup(session.Finish, query.FinishUpdate{})

		ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 30*time.Millisecond)
		defer cancel()
		Expect(reg.StopAll(ctx)).To(MatchError(And(
			ContainSubstring(session.ID()), ContainSubstring("still stopping"),
		)))
	})
})
