package query_test

import (
	stdcontext "context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

const jvmTraceProfile = "trace-capture/jvm_trace"

func jvmTrackOptions() query.TrackOptions {
	return query.TrackOptions{
		Profile:   jvmTraceProfile,
		Kind:      query.KindCapture,
		Params:    map[string]any{"class": "com.example.Listener", "durationMs": 900000.0},
		Labels:    map[string]string{"target": "cycle", "origin": "web"},
		Principal: "admin",
	}
}

func track(reg *query.SessionRegistry, ctx stdcontext.Context, opts query.TrackOptions) *query.Session {
	GinkgoHelper()
	session, err := reg.Track(ctx, opts)
	Expect(err).ToNot(HaveOccurred())
	return session
}

// finishOnStop installs the host teardown most specs use: stop, then Finish.
func finishOnStop(session *query.Session) {
	session.OnStop(func(string) { session.Finish(query.FinishUpdate{Summary: map[string]int{"calls": 3}}) })
}

func waitDone(session *query.Session) {
	GinkgoHelper()
	Eventually(session.Done(), "5s").Should(BeClosed())
}

var _ = Describe("SessionRegistry.Track", func() {
	var store *fakeSessionStore
	var reg *query.SessionRegistry

	BeforeEach(func() {
		store = newFakeSessionStore()
		reg = query.NewSessionRegistry(query.RegistryOptions{Store: store})
	})

	It("records start, running, progress, stopping and stopped through the store", func() {
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		id := session.ID()

		begun, ok := store.record(id)
		Expect(ok).To(BeTrue())
		Expect(begun.SessionStart).To(Equal(query.SessionStart{
			SchemaVersion: 1, ID: id, Profile: jvmTraceProfile, Kind: query.KindCapture, Role: query.SessionRoleCapture,
			Params: jvmTrackOptions().Params, Labels: jvmTrackOptions().Labels, Principal: "admin",
			Owner: reg.Owner(), StartedAt: begun.StartedAt,
		}))
		Expect(begun.State).To(Equal(query.SessionStarting))
		Expect(begun.StopAt.Sub(begun.StartedAt)).To(Equal(query.DefaultMaxDuration), "no StopAt runs for MaxDuration")

		ref := &query.EventsRef{Stream: "probe-1", Kind: "jvm_trace", Low: 1, High: 2, Total: 2}
		Expect(session.Running(query.RunningUpdate{Handle: "Listener.onMessage.1", Events: ref})).To(Succeed())
		session.Progress(query.ProgressUpdate{EventCount: 2, Events: ref})
		finishOnStop(session)
		session.Stop("stopped by admin")
		waitDone(session)

		final := store.status(id)
		Expect(final.State).To(Equal(query.SessionStopped))
		Expect(final.StopReason).To(Equal("stopped by admin"))
		Expect(final.Handle).To(Equal("Listener.onMessage.1"))
		Expect(final.EventCount).To(Equal(int64(2)))
		Expect(final.Events).To(Equal(ref))
		Expect(final.StoppedAt).ToNot(BeNil())
		Expect(string(final.Summary)).To(Equal(`{"calls":3}`))
		Expect(store.statesWritten(id)).To(Equal([]query.SessionState{
			query.SessionRunning, query.SessionStopping, query.SessionStopped,
		}), "progress right after running waits for ProgressEvery; the stop carries it")
	})

	It("records the metadata the host reports once armed, through to the stopped record", func() {
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		metadata := []query.SessionMetadata{
			{Name: "xe.statements", Label: "Started with", Language: "sql", Value: "CREATE EVENT SESSION [t] ON SERVER;"},
		}
		Expect(session.Running(query.RunningUpdate{Handle: "t", Metadata: metadata})).To(Succeed())
		finishOnStop(session)
		session.Stop("stopped by admin")
		waitDone(session)

		encoded, err := json.Marshal(store.status(session.ID()))
		Expect(err).ToNot(HaveOccurred())
		Expect(map[string]any{"snapshot": session.Snapshot().Metadata, "stored": store.status(session.ID()).Metadata, "json": string(encoded)}).To(
			MatchAllKeys(Keys{
				"snapshot": Equal(metadata), "stored": Equal(metadata),
				"json": ContainSubstring(`"metadata":[{"name":"xe.statements","label":"Started with","language":"sql","value":"CREATE EVENT SESSION [t] ON SERVER;"}]`),
			}))
	})

	DescribeTable("refuses metadata that names or shows nothing, or names an entry twice",
		func(metadata []query.SessionMetadata, refused string) {
			session := track(reg, stdcontext.Background(), jvmTrackOptions())
			Expect(session.Running(query.RunningUpdate{Metadata: metadata})).To(MatchError(ContainSubstring(refused)))
			Expect(session.Snapshot()).To(And(HaveField("State", query.SessionStarting), HaveField("Metadata", BeEmpty())))
		},
		Entry("an entry with no name", []query.SessionMetadata{{Label: "Started with", Value: "SELECT 1"}}, "no name"),
		Entry("an entry with no label", []query.SessionMetadata{{Name: "sql", Value: "SELECT 1"}}, `"sql" has no label`),
		Entry("an entry with no value", []query.SessionMetadata{{Name: "sql", Label: "Started with"}}, `"sql" has no value`),
		Entry("a name used twice", []query.SessionMetadata{
			{Name: "sql", Label: "Started with", Value: "SELECT 1"}, {Name: "sql", Label: "Then", Value: "SELECT 2"},
		}, `"sql" twice`),
	)

	It("completes when the deadline elapses", func() {
		stopAt := time.Now().Add(50 * time.Millisecond)
		opts := jvmTrackOptions()
		opts.StopAt = &stopAt
		session := track(reg, stdcontext.Background(), opts)
		finishOnStop(session)
		Expect(session.Running(query.RunningUpdate{})).To(Succeed())

		waitDone(session)
		Expect(store.status(session.ID())).To(And(
			HaveField("State", query.SessionCompleted),
			HaveField("StopReason", query.StopReasonDeadline),
		))
	})

	It("returns from Stop at stopping and finishes as stopped when the host is done", func() {
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		Expect(session.Running(query.RunningUpdate{})).To(Succeed())
		release := make(chan struct{})
		stopped := make(chan string, 1)
		session.OnStop(func(reason string) {
			stopped <- reason
			<-release
			session.Finish(query.FinishUpdate{})
		})

		session.Stop("stopped by admin")
		Expect(session.Snapshot().State).To(Equal(query.SessionStopping))
		Expect(store.status(session.ID()).State).To(Equal(query.SessionStopping))
		Eventually(stopped).Should(Receive(Equal("stopped by admin")))
		Consistently(session.Done(), "50ms").ShouldNot(BeClosed())

		close(release)
		waitDone(session)
		Expect(store.status(session.ID()).State).To(Equal(query.SessionStopped))
	})

	It("cancels arming on a stop during starting and tears down a resource armed after it", func() {
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		armCtx := session.Context()

		session.Stop("stopped by admin")
		Expect(armCtx.Err()).To(MatchError(stdcontext.Canceled), "arming runs under the session context")

		// The arm ignored its context and returned a resource anyway.
		tornDown := make(chan string, 1)
		Expect(session.Running(query.RunningUpdate{Handle: "late-probe"})).To(Succeed())
		session.OnStop(func(reason string) {
			tornDown <- reason
			session.Finish(query.FinishUpdate{})
		})

		Eventually(tornDown).Should(Receive(Equal("stopped by admin")))
		waitDone(session)
		Expect(store.status(session.ID())).To(And(
			HaveField("State", query.SessionStopped),
			HaveField("Handle", "late-probe"),
		))
	})

	It("fails a stop that overruns the profile's StopTimeout", func() {
		reg = query.NewSessionRegistry(query.RegistryOptions{
			Store:       store,
			StopTimeout: func(profile string) time.Duration { return 40 * time.Millisecond },
		})
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		session.OnStop(func(string) {}) // a host that never finishes

		session.Stop("stopped by admin")
		waitDone(session)
		Expect(store.status(session.ID())).To(And(
			HaveField("State", query.SessionFailed),
			HaveField("Error", ContainSubstring("did not finish within 40ms")),
		))
	})

	It("refuses a StopTimeout that is not positive", func() {
		reg = query.NewSessionRegistry(query.RegistryOptions{StopTimeout: func(string) time.Duration { return 0 }})
		_, err := reg.Track(stdcontext.Background(), jvmTrackOptions())
		Expect(err).To(MatchError(ContainSubstring("must be positive")))
	})

	It("moves the deadline on Extend, clamped to MaxDuration from the session's start", func() {
		const maxDuration = 400 * time.Millisecond
		reg = query.NewSessionRegistry(query.RegistryOptions{Store: store, MaxDuration: maxDuration})
		stopAt := time.Now().Add(80 * time.Millisecond)
		opts := jvmTrackOptions()
		opts.StopAt = &stopAt
		session := track(reg, stdcontext.Background(), opts)
		finishOnStop(session)
		startedAt := session.Snapshot().StartedAt

		time.Sleep(50 * time.Millisecond) // a clamp from now would land 50ms past the cap
		settled, err := reg.Extend(session.ID(), time.Now().Add(48*time.Hour))
		Expect(err).ToNot(HaveOccurred())
		Expect(settled).To(BeTemporally("==", startedAt.Add(maxDuration)))
		Expect(*store.status(session.ID()).StopAt).To(BeTemporally("==", settled))
		Consistently(session.Done(), "150ms").ShouldNot(BeClosed())

		_, err = reg.Extend("missing", settled)
		Expect(errors.Is(err, query.ErrSessionNotLive)).To(BeTrue(), "%v", err)
		session.Stop("stopped by admin")
		waitDone(session)
		_, err = reg.Extend(session.ID(), settled)
		Expect(errors.Is(err, query.ErrSessionEnded)).To(BeTrue(), "%v", err)
	})

	It("writes through the start context's route after the caller's context is gone", func() {
		store.route = "env-a"
		requestCtx, endRequest := stdcontext.WithCancel(stdcontext.WithValue(stdcontext.Background(), routeKey{}, "env-a"))
		session := track(reg, requestCtx, jvmTrackOptions())
		endRequest()

		Expect(session.Running(query.RunningUpdate{Handle: "probe"})).To(Succeed())
		finishOnStop(session)
		session.Stop("stopped by admin")
		waitDone(session)

		Expect(store.status(session.ID()).State).To(Equal(query.SessionStopped))
		Expect(reg.PersistFailures()).To(BeZero())
	})

	It("counts a failed status write and carries it as a warning on the next write", func() {
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		store.failUpdates = 1

		Expect(session.Running(query.RunningUpdate{Handle: "probe"})).To(Succeed())
		Expect(reg.PersistFailures()).To(Equal(int64(1)))
		Expect(store.status(session.ID()).State).To(Equal(query.SessionStarting), "the failed write never landed")

		session.Finish(query.FinishUpdate{})
		Expect(store.status(session.ID())).To(And(
			HaveField("State", query.SessionCompleted),
			HaveField("Warning", ContainSubstring("persist running status: store unavailable")),
		))
	})

	It("writes progress at most once per ProgressEvery and flushes the last change", func() {
		reg = query.NewSessionRegistry(query.RegistryOptions{Store: store, ProgressEvery: 150 * time.Millisecond})
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		Expect(session.Running(query.RunningUpdate{})).To(Succeed())
		DeferCleanup(session.Finish, query.FinishUpdate{})

		for count := int64(1); count <= 5; count++ {
			session.Progress(query.ProgressUpdate{EventCount: count})
		}
		Expect(store.status(session.ID()).EventCount).To(BeZero(), "the running write was just made")
		Eventually(func() int64 { return store.status(session.ID()).EventCount }, "2s").Should(Equal(int64(5)))
		Expect(store.statesWritten(session.ID())).To(HaveLen(2), "running, then one flushed progress write")
	})

	It("serializes concurrent heartbeats, progress and finish so nothing is written after the terminal status", func() {
		store.updateDelay = time.Millisecond
		reg = query.NewSessionRegistry(query.RegistryOptions{
			Store: store, HeartbeatEvery: time.Millisecond, ProgressEvery: time.Millisecond,
		})
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		Expect(session.Running(query.RunningUpdate{})).To(Succeed())

		var writers sync.WaitGroup
		for worker := 0; worker < 4; worker++ {
			writers.Add(1)
			go func() {
				defer writers.Done()
				for count := int64(1); count <= 50; count++ {
					session.Progress(query.ProgressUpdate{EventCount: count, Summary: json.RawMessage(`{"n":1}`)})
				}
			}()
		}
		time.Sleep(20 * time.Millisecond)
		session.Finish(query.FinishUpdate{})
		writers.Wait()
		time.Sleep(20 * time.Millisecond)

		states := store.statesWritten(session.ID())
		Expect(states[len(states)-1]).To(Equal(query.SessionCompleted))
		Expect(states[:len(states)-1]).ToNot(ContainElement(query.SessionCompleted))
		Expect(store.status(session.ID()).State).To(Equal(query.SessionCompleted))
	})
})
