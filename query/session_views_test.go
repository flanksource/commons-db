package query_test

import (
	stdcontext "context"
	"errors"
	"time"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SessionRegistry views", func() {
	// startView follows a blocking provider, as a browser live view does.
	startView := func(reg *query.SessionRegistry, providerType string) (*query.Session, error) {
		query.RegisterProvider(&fakeStreamProvider{typ: providerType, block: true})
		return query.ExecuteStream(context.New(), reg, query.Follow(traceProfile(providerType)))
	}

	It("never counts views against MaxSessions, so filter churn cannot block a capture Track", func() {
		const filterChanges = 20
		reg := query.NewSessionRegistry(query.RegistryOptions{
			MaxSessions: 1, HeartbeatEvery: time.Hour, ViewGrace: 30 * time.Millisecond,
		})
		for change := 0; change < filterChanges; change++ {
			view, err := startView(reg, "stream-churn")
			Expect(err).ToNot(HaveOccurred(), "filter change %d", change)
			_, _, unsubscribe := view.Subscribe()
			unsubscribe() // the client re-filtered and never came back to this view
		}

		capture, err := reg.Track(stdcontext.Background(), jvmTrackOptions())
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(capture.Finish, query.FinishUpdate{})
		Eventually(reg.ActiveViews, "5s").Should(BeZero())
		Expect(reg.ReapedViews()).To(Equal(int64(filterChanges)))
	})

	It("refuses a view past MaxViews with an error that is also ErrMaxSessions", func() {
		reg := query.NewSessionRegistry(query.RegistryOptions{MaxViews: 2, MaxSessions: 1})
		for i := 0; i < 2; i++ {
			view, err := startView(reg, "stream-view-cap")
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(view.Stop, "spec cleanup")
		}

		_, err := startView(reg, "stream-view-cap")
		Expect(errors.Is(err, query.ErrMaxViews)).To(BeTrue(), "%v", err)
		Expect(errors.Is(err, query.ErrMaxSessions)).To(BeTrue(), "transports answer both alike")
		capture, err := reg.Track(stdcontext.Background(), jvmTrackOptions())
		Expect(err).ToNot(HaveOccurred(), "the capture budget is separate")
		DeferCleanup(capture.Finish, query.FinishUpdate{})
	})

	It("ends a view on its own timer once its last subscriber leaves, and a resubscribe cancels it", func() {
		const grace = 80 * time.Millisecond
		reg := query.NewSessionRegistry(query.RegistryOptions{HeartbeatEvery: time.Hour, ViewGrace: grace})
		view, err := startView(reg, "stream-reap")
		Expect(err).ToNot(HaveOccurred())
		_, _, first := view.Subscribe()
		waitState(view, query.SessionRunning)
		Consistently(func() query.SessionState { return view.Snapshot().State }, 2*grace).Should(Equal(query.SessionRunning))

		first()
		_, _, second := view.Subscribe()
		Consistently(func() query.SessionState { return view.Snapshot().State }, 2*grace).Should(Equal(query.SessionRunning),
			"a subscriber back within the grace keeps the view")

		_, live, third := view.Subscribe()
		second()
		third()
		left := time.Now()
		waitDone(view)
		Expect(time.Since(left)).To(BeNumerically("<", time.Second), "the heartbeat, an hour away, did not end it")
		Expect(live).To(BeClosed())
		Expect(view.Snapshot()).To(And(
			HaveField("State", query.SessionStopped),
			HaveField("StopReason", ContainSubstring(query.StopReasonReapedIdle)),
			HaveField("StopReason", ContainSubstring(grace.String())),
		))
		Expect(reg.ReapedViews()).To(Equal(int64(1)))
	})
})
