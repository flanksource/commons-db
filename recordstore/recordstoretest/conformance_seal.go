package recordstoretest

import (
	"errors"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
)

func (s *suite) sealSpecs() {
	ginkgo.It("refuses an append to a sealed stream with recordstore.ErrSealed, keeping the rows it holds", func() {
		s.appendRows("run-1", SampleRows(1, 3))
		gomega.Expect(s.backend.Seal(s.ctx, "run-1")).To(gomega.Succeed())

		_, err := s.backend.Append(s.ctx, "run-1", Kind, SampleRows(4, 4))
		gomega.Expect(errors.Is(err, recordstore.ErrSealed)).To(gomega.BeTrue(), "Append: %v", err)
		_, err = s.backend.Append(s.ctx, "run-1", Kind, nil)
		gomega.Expect(errors.Is(err, recordstore.ErrSealed)).To(gomega.BeTrue(), "empty Append: %v", err)
		seqs, rows := Scanned(s.backend, "run-1", 0)
		gomega.Expect(seqs).To(gomega.Equal([]int64{1, 2, 3}))
		gomega.Expect(Normalize(rows)).To(gomega.Equal(Normalize(SampleRows(1, 3))))
	})

	ginkgo.It("reports a sealed stream in its metadata, across a reopen, and seals it again as a no-op", func() {
		s.appendRows("run-1", SampleRows(1, 2))
		gomega.Expect(s.meta("run-1").Sealed).To(gomega.BeFalse())
		gomega.Expect(s.backend.Seal(s.ctx, "run-1")).To(gomega.Succeed())
		gomega.Expect(s.backend.Seal(s.ctx, "run-1")).To(gomega.Succeed())

		s.reopen()
		gomega.Expect(described(s.meta("run-1"))).To(gomega.Equal(recordstore.Meta{
			Stream: "run-1", Kind: Kind, Total: 2, LowSeq: 1, HighSeq: 2, Sealed: true,
		}))
	})

	ginkgo.It("still expires a sealed stream, and starts a reused id unsealed", func() {
		s.appendRows("run-1", SampleRows(1, 1))
		gomega.Expect(s.backend.Seal(s.ctx, "run-1")).To(gomega.Succeed())
		gomega.Expect(s.backend.Expire(s.ctx, "run-1", ExpiryTTL)).To(gomega.Succeed())
		s.harness.Elapse(2 * ExpiryTTL)

		_, err := s.backend.Meta(s.ctx, "run-1")
		gomega.Expect(errors.Is(err, recordstore.ErrNotFound)).To(gomega.BeTrue(), "Meta after expiry: %v", err)
		gomega.Expect(s.appendRows("run-1", SampleRows(2, 2))).To(gomega.Equal(recordstore.Window{From: 1, To: 1}))
		gomega.Expect(s.meta("run-1").Sealed).To(gomega.BeFalse())
	})
}

func (s *suite) sealTailSpecs() {
	ginkgo.It("completes a tail with nil once it has drained a sealed stream", func() {
		notifier := s.notifier()
		gomega.Expect(notifier.Append(s.ctx, "run-1", Kind, SampleRows(1, 3))).Error().ToNot(gomega.HaveOccurred())
		gomega.Expect(notifier.Seal(s.ctx, "run-1")).To(gomega.Succeed())
		run := s.startTail(notifier, "run-1", 1)

		gomega.Expect(run.next(2)).To(gomega.Equal(expected(2, 2, 3)))
		gomega.Eventually(run.done, 5*time.Second).Should(gomega.Receive(gomega.BeNil()))
	})

	ginkgo.It("completes a tail that is waiting for rows when its stream is sealed, after the rows appended before the seal", func() {
		notifier := s.notifier()
		gomega.Expect(notifier.Append(s.ctx, "run-1", Kind, SampleRows(1, 1))).Error().ToNot(gomega.HaveOccurred())
		run := s.startTail(notifier, "run-1", 0)
		gomega.Expect(run.next(1)).To(gomega.Equal(expected(1, 1, 1)))

		gomega.Expect(notifier.Append(s.ctx, "run-1", Kind, SampleRows(2, 3))).Error().ToNot(gomega.HaveOccurred())
		gomega.Expect(notifier.Seal(s.ctx, "run-1")).To(gomega.Succeed())
		gomega.Expect(run.next(2)).To(gomega.Equal(expected(2, 2, 3)))
		gomega.Eventually(run.done, 5*time.Second).Should(gomega.Receive(gomega.BeNil()))
	})
}
