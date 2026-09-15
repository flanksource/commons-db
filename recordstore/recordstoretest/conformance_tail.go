package recordstoretest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
)

// RecheckInterval is how often a conformance tail confirms its stream still
// exists. It is short so the expiry spec ends within the suite's patience.
const RecheckInterval = 20 * time.Millisecond

// tailed is one row a tail delivered.
type tailed struct {
	seq  int64
	name string
}

// tailRun is a Tail running in the background: the rows it delivered, and
// what it returned once it stopped.
type tailRun struct {
	rows    chan tailed
	done    chan error
	stopped chan struct{}
	cancel  context.CancelFunc
}

// startTail tails stream after afterSeq until the spec ends or cancels it.
func (s *suite) startTail(notifier *recordstore.Notifier, stream string, afterSeq int64) tailRun {
	ctx, cancel := context.WithCancel(s.ctx)
	run := tailRun{rows: make(chan tailed), done: make(chan error, 1), stopped: make(chan struct{}), cancel: cancel}
	go func() {
		defer ginkgo.GinkgoRecover()
		defer close(run.stopped)
		run.done <- notifier.Tail(ctx, stream, afterSeq, func(seq int64, row recordstore.Row) error {
			select {
			case run.rows <- tailed{seq: seq, name: fmt.Sprint(row["name"])}:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	ginkgo.DeferCleanup(func() {
		cancel()
		gomega.Eventually(run.stopped, 5*time.Second).Should(gomega.BeClosed())
	})
	return run
}

// next is the next count rows the tail delivers.
func (r tailRun) next(count int) []tailed {
	received := make([]tailed, 0, count)
	for len(received) < count {
		select {
		case row := <-r.rows:
			received = append(received, row)
		case err := <-r.done:
			ginkgo.Fail(fmt.Sprintf("the tail ended after %d of %d rows: %v", len(received), count, err))
		case <-time.After(5 * time.Second):
			ginkgo.Fail(fmt.Sprintf("the tail delivered %d of %d rows", len(received), count))
		}
	}
	return received
}

// expected is the rows first..last as a tail delivers them from seq.
func expected(seq int64, first, last int) []tailed {
	rows := make([]tailed, 0, last-first+1)
	for n := first; n <= last; n++ {
		rows = append(rows, tailed{seq: seq, name: fmt.Sprintf("row-%03d", n)})
		seq++
	}
	return rows
}

func (s *suite) notifier() *recordstore.Notifier {
	notifier, err := recordstore.NewNotifier(s.backend, recordstore.NotifierOptions{RecheckInterval: RecheckInterval})
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	return notifier
}

func (s *suite) tailSpecs() {
	ginkgo.It("tails a stream: the rows it holds, then each row appended after, in seq order", func() {
		notifier := s.notifier()
		gomega.Expect(notifier.Append(s.ctx, "run-1", Kind, SampleRows(1, 3))).Error().ToNot(gomega.HaveOccurred())
		run := s.startTail(notifier, "run-1", 1)

		gomega.Expect(run.next(2)).To(gomega.Equal(expected(2, 2, 3)))
		gomega.Expect(notifier.Append(s.ctx, "run-1", Kind, SampleRows(4, 5))).Error().ToNot(gomega.HaveOccurred())
		gomega.Expect(run.next(2)).To(gomega.Equal(expected(4, 4, 5)))
		gomega.Expect(notifier.Append(s.ctx, "run-1", Kind, SampleRows(6, 6))).Error().ToNot(gomega.HaveOccurred())
		gomega.Expect(run.next(1)).To(gomega.Equal(expected(6, 6, 6)))
	})

	ginkgo.It("never delivers a row a keyed append skipped", func() {
		notifier := s.notifier()
		gomega.Expect(notifier.Append(s.ctx, "run-1", KeyedKind, SampleRows(1, 2))).Error().ToNot(gomega.HaveOccurred())
		run := s.startTail(notifier, "run-1", 0)
		gomega.Expect(run.next(2)).To(gomega.Equal(expected(1, 1, 2)))

		gomega.Expect(notifier.Append(s.ctx, "run-1", KeyedKind, SampleRows(1, 3))).To(gomega.Equal(
			recordstore.AppendResult{Window: recordstore.Window{From: 3, To: 3}, Skipped: 2}))
		gomega.Expect(notifier.Append(s.ctx, "run-1", KeyedKind, SampleRows(2, 4))).To(gomega.Equal(
			recordstore.AppendResult{Window: recordstore.Window{From: 4, To: 4}, Skipped: 2}))
		gomega.Expect(run.next(2)).To(gomega.Equal([]tailed{{seq: 3, name: "row-003"}, {seq: 4, name: "row-004"}}))
	})

	ginkgo.It("returns nil once its context is cancelled", func() {
		notifier := s.notifier()
		gomega.Expect(notifier.Append(s.ctx, "run-1", Kind, SampleRows(1, 1))).Error().ToNot(gomega.HaveOccurred())
		run := s.startTail(notifier, "run-1", 0)
		gomega.Expect(run.next(1)).To(gomega.Equal(expected(1, 1, 1)))

		run.cancel()
		gomega.Eventually(run.done, 5*time.Second).Should(gomega.Receive(gomega.BeNil()))
	})

	ginkgo.It("ends with recordstore.ErrNotFound once the stream it tails expires", func() {
		notifier := s.notifier()
		gomega.Expect(notifier.Append(s.ctx, "run-1", Kind, SampleRows(1, 1))).Error().ToNot(gomega.HaveOccurred())
		run := s.startTail(notifier, "run-1", 0)
		gomega.Expect(run.next(1)).To(gomega.Equal(expected(1, 1, 1)))

		gomega.Expect(notifier.Expire(s.ctx, "run-1", ExpiryTTL)).To(gomega.Succeed())
		s.harness.Elapse(2 * ExpiryTTL)
		var err error
		gomega.Eventually(run.done, 5*time.Second).Should(gomega.Receive(&err))
		gomega.Expect(errors.Is(err, recordstore.ErrNotFound)).To(gomega.BeTrue(), "Tail: %v", err)
	})

	ginkgo.It("refuses to tail a stream that does not exist", func() {
		err := s.notifier().Tail(s.ctx, "missing", 0, func(int64, recordstore.Row) error { return nil })
		gomega.Expect(errors.Is(err, recordstore.ErrNotFound)).To(gomega.BeTrue(), "Tail: %v", err)
	})

	ginkgo.It("stops a tail at the callback's error and returns it", func() {
		notifier := s.notifier()
		gomega.Expect(notifier.Append(s.ctx, "run-1", Kind, SampleRows(1, 2))).Error().ToNot(gomega.HaveOccurred())
		stop := errors.New("stop")
		err := notifier.Tail(s.ctx, "run-1", 0, func(int64, recordstore.Row) error { return stop })
		gomega.Expect(errors.Is(err, stop)).To(gomega.BeTrue(), "Tail: %v", err)
	})
}
