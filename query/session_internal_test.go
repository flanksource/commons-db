package query

import (
	stdcontext "context"
	"errors"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func newTestSession(id string, maxEvents int) *Session {
	return NewSession(SessionOptions{
		ID:        id,
		Profile:   Profile{Name: "sess-test", Trace: &TraceSpec{MaxEvents: maxEvents}},
		MaxEvents: maxEvents,
	})
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

		s.markRunning()
		Expect(s.Snapshot().State).To(Equal(SessionRunning))

		s.markDone(nil)
		snap := s.Snapshot()
		Expect(snap.State).To(Equal(SessionCompleted))
		Expect(snap.StoppedAt).ToNot(BeNil())
		Eventually(live).Should(BeClosed())
	})

	It("records the error on failure", func() {
		s := newTestSession("fail", 10)
		s.markRunning()
		s.markDone(errors.New("stream broke"))

		snap := s.Snapshot()
		Expect(snap.State).To(Equal(SessionFailed))
		Expect(snap.Error).To(ContainSubstring("stream broke"))
	})

	It("Stop wins over a later markDone", func() {
		s := newTestSession("stop", 10)
		s.markRunning()
		s.Stop()
		s.markDone(stdcontext.Canceled)

		Expect(s.Snapshot().State).To(Equal(SessionStopped))
	})

	It("notifies OnTransition for every state change", func() {
		var states []SessionState
		s := NewSession(SessionOptions{
			ID:           "hook",
			Profile:      Profile{Name: "sess-test"},
			MaxEvents:    10,
			OnTransition: func(info SessionInfo) { states = append(states, info.State) },
		})
		s.markRunning()
		s.markDone(nil)
		Expect(states).To(Equal([]SessionState{SessionRunning, SessionCompleted}))
	})

	It("delivers every event to the OnEvent hook regardless of ring eviction", func() {
		var got []int64
		s := NewSession(SessionOptions{
			ID:        "onevent",
			Profile:   Profile{Name: "sess-test"},
			MaxEvents: 2,
			OnEvent:   func(e Event) { got = append(got, e.Sequence) },
		})
		for i := 0; i < 4; i++ {
			s.Emit(Event{Row: Row{"i": i}})
		}
		Expect(got).To(Equal([]int64{1, 2, 3, 4}))
	})
})

var _ = Describe("SessionRegistry", func() {
	newRunning := func(id string) *Session {
		s := newTestSession(id, 10)
		s.markRunning()
		return s
	}

	It("rejects new sessions at MaxSessions, counting only active ones", func() {
		r := NewSessionRegistry(RegistryOptions{MaxSessions: 2})
		Expect(r.Add(newRunning("a"))).To(Succeed())
		Expect(r.Add(newRunning("b"))).To(Succeed())
		Expect(r.Add(newRunning("c"))).To(MatchError(ContainSubstring("max sessions")))

		got, ok := r.Get("a")
		Expect(ok).To(BeTrue())
		got.markDone(nil)
		Expect(r.Add(newRunning("c"))).To(Succeed())
	})

	It("prunes the oldest terminal sessions beyond RetainDone", func() {
		r := NewSessionRegistry(RegistryOptions{MaxSessions: 100, RetainDone: 2})
		for i := 0; i < 4; i++ {
			s := newTestSession(fmt.Sprintf("t-%d", i), 10)
			s.markRunning()
			s.markDone(nil)
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

		r.StopAll()
		Expect(a.Snapshot().State).To(Equal(SessionStopped))
		Expect(b.Snapshot().State).To(Equal(SessionStopped))
	})

	It("clamps profile limits to the server caps", func() {
		r := NewSessionRegistry(RegistryOptions{MaxDuration: time.Minute, MaxEvents: 5})
		Expect(r.ClampDuration(time.Hour)).To(Equal(time.Minute))
		Expect(r.ClampDuration(time.Second)).To(Equal(time.Second))
		Expect(r.ClampEvents(100)).To(Equal(5))
		Expect(r.ClampEvents(3)).To(Equal(3))
	})
})
