package recordstore_test

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type watchedBackend struct {
	recordstore.Backend
	started chan struct{}
	stopped chan struct{}
	closed  atomic.Int32
	failure error
}

func (b *watchedBackend) WatchChanges(ctx context.Context, _ func()) error {
	close(b.started)
	defer close(b.stopped)
	if b.failure != nil {
		return b.failure
	}
	<-ctx.Done()
	return nil
}

func (b *watchedBackend) Close() error {
	b.closed.Add(1)
	return b.Backend.Close()
}

var _ = Describe("Notifier ownership", func() {
	It("returns an observation failure when its watcher is released", func() {
		source := &watchedBackend{Backend: openMemoryKV(), started: make(chan struct{}), stopped: make(chan struct{}), failure: context.DeadlineExceeded}
		notifier, err := recordstore.NewNotifier(source, recordstore.NotifierOptions{RecheckInterval: time.Hour})
		Expect(err).NotTo(HaveOccurred())
		Eventually(source.stopped).Should(BeClosed())
		Expect(notifier.StopWatching()).To(MatchError(context.DeadlineExceeded))
		Expect(notifier.Close()).To(MatchError(ContainSubstring(context.DeadlineExceeded.Error())))
		Expect(source.closed.Load()).To(Equal(int32(1)))
	})
	It("can stop its watch without closing a shared backend, and closes its backend once", func() {
		source := &watchedBackend{Backend: openMemoryKV(), started: make(chan struct{}), stopped: make(chan struct{})}
		notifier, err := recordstore.NewNotifier(source, recordstore.NotifierOptions{RecheckInterval: time.Hour})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(notifier.Close)
		Eventually(source.started).Should(BeClosed())
		Expect(notifier.StopWatching()).To(Succeed())
		Expect(source.stopped).To(BeClosed())
		Expect(source.closed.Load()).To(BeZero())
		Expect(source.Append(context.Background(), "still-open", recordstoretest.Kind, recordstoretest.SampleRows(1, 1))).Error().NotTo(HaveOccurred())
		Expect(notifier.StopWatching()).To(Succeed())
		Expect(notifier.Close()).To(Succeed())
		Expect(notifier.Close()).To(Succeed())
		Expect(source.closed.Load()).To(Equal(int32(1)))
	})
})
