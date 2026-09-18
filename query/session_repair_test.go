package query_test

import (
	stdcontext "context"
	"errors"
	"time"

	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SessionRegistry status repair", func() {
	It("retries a refused terminal write on the heartbeat until the store holds the final state", func() {
		const refusals = 3
		store := newFakeSessionStore()
		reg := query.NewSessionRegistry(query.RegistryOptions{Store: store, HeartbeatEvery: 20 * time.Millisecond})
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		Expect(session.Running(query.RunningUpdate{Handle: "probe"})).To(Succeed())

		store.failUpdates = refusals
		session.Stop("stopped by admin") // the stopping write is refused
		session.Finish(query.FinishUpdate{Summary: map[string]int{"calls": 3}})
		Expect(store.status(session.ID()).State).To(Equal(query.SessionRunning))

		Eventually(func() query.SessionState { return store.status(session.ID()).State }, "5s").
			Should(Equal(query.SessionStopped))
		final := store.status(session.ID())
		Expect(final).To(And(
			HaveField("StopReason", "stopped by admin"), HaveField("Handle", "probe"),
			HaveField("Warning", ContainSubstring("store unavailable")),
		))
		Expect(string(final.Summary)).To(Equal(`{"calls":3}`))
		Expect(reg.PersistFailures()).To(BeNumerically(">=", refusals))
		written := len(store.writesFor(session.ID()))
		Consistently(func() int { return len(store.writesFor(session.ID())) }, "100ms").Should(Equal(written),
			"a repaired status is not written again")
	})
})

var _ = Describe("Session.Abort", func() {
	var reg *query.SessionRegistry

	BeforeEach(func() {
		reg = query.NewSessionRegistry(query.RegistryOptions{Store: newFakeSessionStore()})
	})

	It("runs the OnStop handler so the host releases what it armed", func() {
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		Expect(session.Running(query.RunningUpdate{Handle: "probe"})).To(Succeed())
		released := make(chan string, 1)
		session.OnStop(func(reason string) { released <- reason })

		session.Abort(errors.New("event log unwritable"))
		waitDone(session)
		Eventually(released).Should(Receive(Equal("aborted: event log unwritable")))
		Expect(session.Snapshot().State).To(Equal(query.SessionFailed))
	})

	It("runs an OnStop handler installed after the abort at once", func() {
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		session.Abort(errors.New("event log unwritable"))
		waitDone(session)

		released := make(chan string, 1)
		session.OnStop(func(reason string) { released <- reason })
		Eventually(released).Should(Receive(Equal("aborted: event log unwritable")))
	})

	It("does not run the handler twice after a stop that overran its timeout", func() {
		reg = query.NewSessionRegistry(query.RegistryOptions{StopTimeout: func(string) time.Duration { return 30 * time.Millisecond }})
		session := track(reg, stdcontext.Background(), jvmTrackOptions())
		calls := make(chan string, 2)
		session.OnStop(func(reason string) { calls <- reason })

		session.Stop("stopped by admin")
		waitDone(session)
		Eventually(calls).Should(Receive(Equal("stopped by admin")))
		Consistently(calls, "80ms").ShouldNot(Receive())
	})
})
