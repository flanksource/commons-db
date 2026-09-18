package query_test

import (
	"context"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
)

type managedRunFake struct {
	samples  atomic.Int64
	stops    atomic.Int64
	detaches atomic.Int64
	finished chan struct{}
	metadata []query.SessionMetadata
}

func newManagedRunFake() *managedRunFake {
	return &managedRunFake{finished: make(chan struct{})}
}

func (f *managedRunFake) Sample(context.Context) (any, error) {
	return f.samples.Add(1), nil
}

func (f *managedRunFake) Status() query.ManagedStatus {
	return query.ManagedStatus{
		Handle: "probe-1", EventCount: f.samples.Load(), Summary: map[string]any{"samples": f.samples.Load()}, Metadata: f.metadata,
	}
}

func (f *managedRunFake) Stop(context.Context) (query.ManagedFinish, error) {
	f.stops.Add(1)
	return query.ManagedFinish{ManagedStatus: query.ManagedStatus{Summary: f.Status().Summary}, Result: "stopped"}, nil
}

func (f *managedRunFake) Detach(context.Context) (query.ManagedFinish, error) {
	f.detaches.Add(1)
	return query.ManagedFinish{ManagedStatus: query.ManagedStatus{Summary: f.Status().Summary}}, nil
}

func (f *managedRunFake) Finished() <-chan struct{} { return f.finished }

func managedTrackOptions() query.TrackOptions {
	stopAt := time.Now().Add(time.Minute)
	return query.TrackOptions{Profile: "trace-capture/test", Kind: query.KindCapture, StopAt: &stopAt}
}

var _ = Describe("SessionRegistry managed captures", func() {
	It("arms a run and records its initial status", func() {
		registry := query.NewSessionRegistry(query.RegistryOptions{})
		run := newManagedRunFake()

		managed, err := registry.Manage(context.Background(), query.ManageOptions{Track: managedTrackOptions()}, func(context.Context) (query.ManagedRun, error) {
			return run, nil
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(managed.Session().Snapshot()).To(And(
			HaveField("State", query.SessionRunning),
			HaveField("Handle", "probe-1"),
		))
		result, err := managed.Stop(context.Background())
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal("stopped"))
		Expect(run.stops.Load()).To(Equal(int64(1)))
		Expect(managed.Session().Snapshot().State).To(Equal(query.SessionCompleted))
	})

	It("records the metadata the armed run reports", func() {
		registry := query.NewSessionRegistry(query.RegistryOptions{})
		run := newManagedRunFake()
		run.metadata = []query.SessionMetadata{{Name: "xe.statements", Label: "Started with", Language: "sql", Value: "CREATE EVENT SESSION [t] ON SERVER;"}}

		managed, err := registry.Manage(context.Background(), query.ManageOptions{Track: managedTrackOptions()}, func(context.Context) (query.ManagedRun, error) {
			return run, nil
		})

		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _, _ = managed.Stop(context.Background()) })
		Expect(managed.Session().Snapshot().Metadata).To(Equal(run.metadata))
	})

	It("stops the run and fails the session when the armed run reports metadata it cannot record", func() {
		registry := query.NewSessionRegistry(query.RegistryOptions{})
		run := newManagedRunFake()
		run.metadata = []query.SessionMetadata{{Name: "xe.statements", Label: "Started with"}}

		_, err := registry.Manage(context.Background(), query.ManageOptions{Track: managedTrackOptions()}, func(context.Context) (query.ManagedRun, error) {
			return run, nil
		})

		Expect(err).To(MatchError(ContainSubstring(`"xe.statements" has no value`)))
		Expect(run.stops.Load()).To(Equal(int64(1)))
	})

	It("serializes polling and explicit sampling and stops the run once", func() {
		const pollEvery = 10 * time.Millisecond
		registry := query.NewSessionRegistry(query.RegistryOptions{})
		run := newManagedRunFake()
		managed, err := registry.Manage(context.Background(), query.ManageOptions{
			Track: managedTrackOptions(), PollEvery: pollEvery, PullOnPoll: true,
		}, func(context.Context) (query.ManagedRun, error) { return run, nil })
		Expect(err).NotTo(HaveOccurred())

		Eventually(run.samples.Load).Should(BeNumerically(">=", 1))
		_, err = managed.Sample(context.Background())
		Expect(err).NotTo(HaveOccurred())
		managed.Session().Stop("operator requested")
		Eventually(managed.Session().Done()).Should(BeClosed())
		_, _ = managed.Stop(context.Background())
		Expect(run.stops.Load()).To(Equal(int64(1)))
		Expect(managed.Session().Snapshot().State).To(Equal(query.SessionStopped))
	})

	It("detaches a recoverable run without stopping it", func() {
		registry := query.NewSessionRegistry(query.RegistryOptions{})
		run := newManagedRunFake()
		managed, err := registry.Manage(context.Background(), query.ManageOptions{
			Track: managedTrackOptions(), Recoverable: true,
		}, func(context.Context) (query.ManagedRun, error) { return run, nil })
		Expect(err).NotTo(HaveOccurred())

		Expect(managed.Detach(context.Background(), "serve restarting")).To(Succeed())
		Eventually(managed.Session().Done()).Should(BeClosed())
		Expect(run.detaches.Load()).To(Equal(int64(1)))
		Expect(run.stops.Load()).To(BeZero())
		Expect(managed.Session().Snapshot()).To(And(
			HaveField("State", query.SessionInterrupted),
			HaveField("StopReason", "serve restarting"),
		))
	})

	It("fails the tracked session when arming fails", func() {
		registry := query.NewSessionRegistry(query.RegistryOptions{})
		armErr := context.DeadlineExceeded

		managed, err := registry.Manage(context.Background(), query.ManageOptions{Track: managedTrackOptions()}, func(context.Context) (query.ManagedRun, error) {
			return nil, armErr
		})

		Expect(managed).To(BeNil())
		Expect(err).To(MatchError(armErr))
		Expect(registry.List()).To(HaveLen(1))
		Expect(registry.List()[0]).To(And(
			HaveField("State", query.SessionFailed),
			HaveField("Error", ContainSubstring(context.DeadlineExceeded.Error())),
		))
	})

	It("records an abort cause after stopping the run", func() {
		registry := query.NewSessionRegistry(query.RegistryOptions{})
		run := newManagedRunFake()
		managed, err := registry.Manage(context.Background(), query.ManageOptions{Track: managedTrackOptions()}, func(context.Context) (query.ManagedRun, error) {
			return run, nil
		})
		Expect(err).NotTo(HaveOccurred())

		cause := context.DeadlineExceeded
		_, err = managed.Abort(cause)
		Expect(err).To(MatchError(cause))
		Expect(run.stops.Load()).To(Equal(int64(1)))
		Expect(managed.Session().Snapshot()).To(And(
			HaveField("State", query.SessionFailed),
			HaveField("Error", ContainSubstring(cause.Error())),
		))
	})

	It("requires a reason before detaching", func() {
		registry := query.NewSessionRegistry(query.RegistryOptions{})
		run := newManagedRunFake()
		managed, err := registry.Manage(context.Background(), query.ManageOptions{
			Track: managedTrackOptions(), Recoverable: true,
		}, func(context.Context) (query.ManagedRun, error) { return run, nil })
		Expect(err).NotTo(HaveOccurred())

		Expect(managed.Detach(context.Background(), "")).To(MatchError(ContainSubstring("reason is required")))
		Expect(run.detaches.Load()).To(BeZero())
		_, err = managed.Stop(context.Background())
		Expect(err).NotTo(HaveOccurred())
	})

	It("detaches only recoverable runs during a recoverable shutdown", func() {
		registry := query.NewSessionRegistry(query.RegistryOptions{})
		recoverable := newManagedRunFake()
		nonRecoverable := newManagedRunFake()
		first, err := registry.Manage(context.Background(), query.ManageOptions{
			Track: managedTrackOptions(), Recoverable: true,
		}, func(context.Context) (query.ManagedRun, error) { return recoverable, nil })
		Expect(err).NotTo(HaveOccurred())
		second, err := registry.Manage(context.Background(), query.ManageOptions{
			Track: managedTrackOptions(),
		}, func(context.Context) (query.ManagedRun, error) { return nonRecoverable, nil })
		Expect(err).NotTo(HaveOccurred())

		Expect(registry.Shutdown(context.Background(), query.ShutdownOptions{
			DetachRecoverable: true,
			Reason:            "serve restarting",
		})).To(Succeed())
		Expect(recoverable.detaches.Load()).To(Equal(int64(1)))
		Expect(recoverable.stops.Load()).To(BeZero())
		Expect(nonRecoverable.stops.Load()).To(Equal(int64(1)))
		Expect(first.Session().Snapshot().State).To(Equal(query.SessionInterrupted))
		Expect(second.Session().Snapshot().State).To(Equal(query.SessionStopped))
	})
})
