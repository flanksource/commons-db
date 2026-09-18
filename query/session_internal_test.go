package query

import (
	stdcontext "context"
	"errors"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func newTestSession(id string, maxEvents int) *Session {
	GinkgoHelper()
	s, err := NewSession(SessionOptions{
		ID:        id,
		Profile:   Profile{Name: "sess-test", Trace: &TraceSpec{MaxEvents: maxEvents}},
		Kind:      KindTrace,
		Role:      SessionRoleCapture,
		MaxEvents: maxEvents,
	})
	Expect(err).ToNot(HaveOccurred())
	return s
}

func markRunning(s *Session) {
	GinkgoHelper()
	Expect(s.Running(RunningUpdate{})).To(Succeed())
}

var _ = Describe("Session", func() {
	It("evicts the oldest events at MaxEvents while keeping sequence monotonic", func() {
		s := newTestSession("ring", 3)
		for i := 0; i < 5; i++ {
			s.Emit(Event{Row: Row{"i": i}})
		}

		events := s.Events()
		Expect(events).To(HaveLen(3))
		Expect(events[0].Sequence).To(Equal(int64(3)))
		Expect(events[2].Sequence).To(Equal(int64(5)))
		Expect(s.Snapshot().EventCount).To(Equal(int64(5)))
	})

	It("subscribes with a gap-free replay-then-live handoff", func() {
		s := newTestSession("sub", 10)
		s.Emit(Event{Row: Row{"i": 1}})
		s.Emit(Event{Row: Row{"i": 2}})

		replay, live, cancel := s.Subscribe()
		defer cancel()
		Expect(replay).To(HaveLen(2))

		s.Emit(Event{Row: Row{"i": 3}})
		Expect(<-live).To(HaveField("Sequence", int64(3)))
	})

	It("replays only what a resuming subscriber has not already seen", func() {
		s := newTestSession("resume", 10)
		for i := 1; i <= 3; i++ {
			s.Emit(Event{Row: Row{"i": i}})
		}

		replay, live, cancel := s.SubscribeFrom(2)
		defer cancel()
		Expect(replay).To(HaveLen(1))
		Expect(replay[0].Sequence).To(Equal(int64(3)))

		s.Emit(Event{Row: Row{"i": 4}})
		Expect(<-live).To(HaveField("Sequence", int64(4)))
	})

	It("serves what the ring still holds when the resumed sequence was evicted", func() {
		s := newTestSession("resume-evicted", 2)
		for i := 1; i <= 4; i++ {
			s.Emit(Event{Row: Row{"i": i}})
		}

		replay, _, cancel := s.SubscribeFrom(1)
		defer cancel()
		Expect(replay).To(HaveLen(2))
		Expect(replay[0].Sequence).To(Equal(int64(3)), "events 2 and 3 are gone either way; serving what survives beats serving nothing")
	})

	It("does not block Emit on a slow subscriber", func(ctx SpecContext) {
		s := newTestSession("slow", 1000)
		_, _, cancel := s.Subscribe()
		defer cancel()

		done := make(chan struct{})
		go func() {
			defer close(done)
			for i := 0; i < 400; i++ { // exceeds the subscriber channel capacity
				s.Emit(Event{Row: Row{"i": i}})
			}
		}()
		Eventually(done).WithContext(ctx).Should(BeClosed())
	}, SpecTimeout(5*time.Second))

	It("transitions starting → running → completed and closes subscribers", func() {
		s := newTestSession("done", 10)
		Expect(s.Snapshot().State).To(Equal(SessionStarting))

		_, live, cancel := s.Subscribe()
		defer cancel()

		markRunning(s)
		Expect(s.Snapshot().State).To(Equal(SessionRunning))

		s.Finish(FinishUpdate{})
		snap := s.Snapshot()
		Expect(snap.State).To(Equal(SessionCompleted))
		Expect(snap.StoppedAt).ToNot(BeNil())
		Eventually(live).Should(BeClosed())
		Expect(s.Done()).To(BeClosed())
	})

	It("records the error on failure", func() {
		s := newTestSession("fail", 10)
		markRunning(s)
		s.Finish(FinishUpdate{Err: errors.New("stream broke")})

		snap := s.Snapshot()
		Expect(snap.State).To(Equal(SessionFailed))
		Expect(snap.Error).To(ContainSubstring("stream broke"))
	})

	It("Abort fails an active session and closes subscribers", func() {
		s := newTestSession("abort", 10)
		markRunning(s)
		_, live, cancel := s.Subscribe()
		defer cancel()

		s.Abort(errors.New("event log unwritable"))
		snap := s.Snapshot()
		Expect(snap.State).To(Equal(SessionFailed))
		Expect(snap.Error).To(ContainSubstring("event log unwritable"))
		Eventually(live).Should(BeClosed())

		s.Finish(FinishUpdate{})
		Expect(s.Snapshot().State).To(Equal(SessionFailed), "a later Finish never resurrects an aborted session")
	})

	It("Stop wins over a later Finish", func() {
		s := newTestSession("stop", 10)
		markRunning(s)
		s.Stop("stopped by admin")
		s.Finish(FinishUpdate{Err: stdcontext.Canceled})

		Expect(s.Snapshot().State).To(Equal(SessionStopped))
		Expect(s.Snapshot().StopReason).To(Equal("stopped by admin"))
	})

	It("refuses Running once the session has ended", func() {
		s := newTestSession("ended", 10)
		s.Finish(FinishUpdate{})
		Expect(errors.Is(s.Running(RunningUpdate{Handle: "late"}), ErrSessionEnded)).To(BeTrue())
	})

	DescribeTable("rejects incomplete session options",
		func(opts SessionOptions, message string) {
			_, err := NewSession(opts)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("no id", SessionOptions{Profile: Profile{Name: "p"}, Kind: KindTrace, Role: SessionRoleView}, "id is required"),
		Entry("no profile", SessionOptions{ID: "x", Kind: KindTrace, Role: SessionRoleView}, "profile name is required"),
		Entry("no kind", SessionOptions{ID: "x", Profile: Profile{Name: "p"}, Role: SessionRoleView}, "kind is required"),
		Entry("unknown role", SessionOptions{ID: "x", Profile: Profile{Name: "p"}, Kind: KindTrace, Role: "watch"}, `role "watch"`),
	)

	DescribeTable("caps a status payload",
		func(value any, wantJSON string, wantWarning string) {
			data, warning := encodeStatusPayload("summary", value)
			Expect(string(data)).To(Equal(wantJSON))
			Expect(warning).To(ContainSubstring(wantWarning))
		},
		Entry("under the cap as JSON", map[string]int{"calls": 3}, `{"calls":3}`, ""),
		Entry("over the cap as an omitted marker", strings.Repeat("x", maxStatusPayloadBytes), `{"omitted":16386}`, "summary omitted: 16386 bytes"),
		Entry("unmarshalable as a warning", map[string]any{"bad": func() {}}, "", "summary not recorded"),
	)
})

var _ = Describe("sortAndLimit", func() {
	It("orders numeric strings numerically, not lexicographically", func() {
		rows := []Row{{"d": "96.8"}, {"d": "136.4"}, {"d": "7"}}
		sorted, _ := sortAndLimit(rows, "d", 2)
		Expect(sorted).To(Equal([]Row{{"d": "136.4"}, {"d": "96.8"}}))
	})

	It("leaves rows untouched without sortBy or limit", func() {
		rows := []Row{{"d": 2}, {"d": 1}}
		sorted, cut := sortAndLimit(rows, "", 0)
		Expect(sorted).To(Equal([]Row{{"d": 2}, {"d": 1}}))
		Expect(cut).To(BeFalse())
	})

	// A snapshot that dropped its tail must not be presented as the whole of it.
	It("reports that the limit removed rows", func() {
		_, cut := sortAndLimit([]Row{{"d": 3}, {"d": 2}, {"d": 1}}, "d", 2)
		Expect(cut).To(BeTrue())
	})

	It("does not report a limit the rows never reached", func() {
		_, cut := sortAndLimit([]Row{{"d": 2}, {"d": 1}}, "d", 5)
		Expect(cut).To(BeFalse())
	})
})

var _ = Describe("SessionRegistry", func() {
	newRunning := func(id string) *Session {
		s := newTestSession(id, 10)
		markRunning(s)
		return s
	}

	It("rejects new sessions at MaxSessions, counting only active ones", func() {
		r := NewSessionRegistry(RegistryOptions{MaxSessions: 2})
		Expect(r.Add(newRunning("a"))).To(Succeed())
		Expect(r.Add(newRunning("b"))).To(Succeed())
		Expect(r.Add(newRunning("c"))).To(MatchError(ContainSubstring("max sessions")))

		got, ok := r.Get("a")
		Expect(ok).To(BeTrue())
		got.Finish(FinishUpdate{})
		Expect(r.Add(newRunning("c"))).To(Succeed())
	})

	It("prunes the oldest terminal sessions beyond RetainDone", func() {
		r := NewSessionRegistry(RegistryOptions{MaxSessions: 100, RetainDone: 2})
		for i := 0; i < 4; i++ {
			s := newTestSession(fmt.Sprintf("t-%d", i), 10)
			markRunning(s)
			s.Finish(FinishUpdate{})
			Expect(r.Add(s)).To(Succeed())
		}

		_, ok := r.Get("t-0")
		Expect(ok).To(BeFalse())
		_, ok = r.Get("t-3")
		Expect(ok).To(BeTrue())
		Expect(r.List()).To(HaveLen(2))
	})

	It("stops all active sessions", func() {
		r := NewSessionRegistry(RegistryOptions{})
		a, b := newRunning("a"), newRunning("b")
		Expect(r.Add(a)).To(Succeed())
		Expect(r.Add(b)).To(Succeed())

		Expect(r.StopAll(stdcontext.Background())).To(Succeed())
		Expect(a.Snapshot().State).To(Equal(SessionStopped))
		Expect(b.Snapshot().State).To(Equal(SessionStopped))
	})

	It("returns an idempotent cleanup for a prepared read", func() {
		releases := 0
		r := NewSessionRegistry(RegistryOptions{BeforeRead: func(stdcontext.Context, Profile, map[string]any) (func(), error) {
			return func() { releases++ }, nil
		}})

		release, err := r.prepareRead(stdcontext.Background(), Profile{Name: "prepared"}, nil)
		Expect(err).ToNot(HaveOccurred())
		release()
		release()
		Expect(releases).To(Equal(1))
	})

	It("releases a cleanup returned alongside a preparation error", func() {
		releases := 0
		r := NewSessionRegistry(RegistryOptions{BeforeRead: func(stdcontext.Context, Profile, map[string]any) (func(), error) {
			return func() { releases++ }, errors.New("index unavailable")
		}})

		release, err := r.prepareRead(stdcontext.Background(), Profile{Name: "prepared"}, nil)
		Expect(errors.Is(err, ErrPrepareRead)).To(BeTrue(), "%v", err)
		release()
		Expect(releases).To(Equal(1))
	})

	It("normalizes an absent read cleanup", func() {
		r := NewSessionRegistry(RegistryOptions{BeforeRead: func(stdcontext.Context, Profile, map[string]any) (func(), error) {
			return nil, nil
		}})

		release, err := r.prepareRead(stdcontext.Background(), Profile{Name: "prepared"}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(release).ToNot(BeNil())
		Expect(release).ToNot(Panic())
	})

	It("clamps profile limits to the server caps", func() {
		r := NewSessionRegistry(RegistryOptions{MaxDuration: time.Minute, MaxEvents: 5})
		Expect(r.ClampDuration(time.Hour)).To(Equal(time.Minute))
		Expect(r.ClampDuration(time.Second)).To(Equal(time.Second))
		Expect(r.ClampEvents(100)).To(Equal(5))
		Expect(r.ClampEvents(3)).To(Equal(3))
	})
})
