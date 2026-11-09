package query_test

import (
	"context"
	"time"

	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type blockedManagedRun struct {
	*managedRunFake
	started      chan struct{}
	release      chan struct{}
	ignoreCancel bool
}

func (r *blockedManagedRun) Sample(ctx context.Context) (any, error) {
	close(r.started)
	if r.ignoreCancel {
		<-r.release
		return nil, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.release:
		return nil, nil
	}
}

var _ = Describe("Managed capture cancellation", func() {
	It("cancels a running sample before stopping and rejects later samples", func() {
		run := &blockedManagedRun{managedRunFake: newManagedRunFake(), started: make(chan struct{}), release: make(chan struct{})}
		DeferCleanup(func() { close(run.release) })
		registry := query.NewSessionRegistry(query.RegistryOptions{})
		managed, err := registry.Manage(context.Background(), query.ManageOptions{Track: managedTrackOptions()}, func(context.Context) (query.ManagedRun, error) { return run, nil })
		Expect(err).NotTo(HaveOccurred())
		sampled, stopped := make(chan error, 1), make(chan error, 1)
		go func() { _, err := managed.Sample(context.Background()); sampled <- err }()
		Eventually(run.started).Should(BeClosed())
		go func() { _, err := managed.Stop(context.Background()); stopped <- err }()
		Eventually(stopped, time.Second).Should(Receive(BeNil()))
		Eventually(sampled).Should(Receive(MatchError(context.Canceled)))
		_, err = managed.Sample(context.Background())
		Expect(err).To(MatchError(ContainSubstring(query.ErrSessionEnded.Error())))
		Expect(run.stops.Load()).To(Equal(int64(1)))
	})

	It("includes the wait for an unresponsive sample in the stop deadline", func() {
		run := &blockedManagedRun{managedRunFake: newManagedRunFake(), started: make(chan struct{}), release: make(chan struct{}), ignoreCancel: true}
		DeferCleanup(func() { close(run.release) })
		registry := query.NewSessionRegistry(query.RegistryOptions{})
		managed, err := registry.Manage(context.Background(), query.ManageOptions{Track: managedTrackOptions(), StopTimeout: 50 * time.Millisecond}, func(context.Context) (query.ManagedRun, error) { return run, nil })
		Expect(err).NotTo(HaveOccurred())
		sampled, stopped := make(chan error, 1), make(chan error, 1)
		go func() { _, err := managed.Sample(context.Background()); sampled <- err }()
		Eventually(run.started).Should(BeClosed())
		go func() { _, err := managed.Stop(context.Background()); stopped <- err }()
		Eventually(stopped, time.Second).Should(Receive(MatchError(ContainSubstring(context.DeadlineExceeded.Error()))))
		Expect(managed.Session().Snapshot().State).To(Equal(query.SessionFailed))
		Expect(run.stops.Load()).To(BeZero(), "Stop must not run concurrently with Sample")
	})
})
