package recordstoretest

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
)

// appendGap separates the appends of a trim spec, so each append has an
// instant of its own to trim before.
const appendGap = time.Minute

// described is meta with the fields a spec does not pin cleared.
func described(meta recordstore.Meta) recordstore.Meta {
	meta.Generation, meta.UpdatedAt, meta.ExpiresAt = "", time.Time{}, nil
	return meta
}

func (s *suite) trimSpecs() {
	ginkgo.It("trims the rows appended before an instant, keeping the rest under their seqs", func() {
		s.appendRows("run-1", SampleRows(1, 2))
		s.harness.Advance(appendGap)
		second := s.harness.Now()
		s.appendRows("run-1", SampleRows(3, 4))
		s.harness.Advance(appendGap)
		s.appendRows("run-1", SampleRows(5, 5))

		meta, err := s.backend.Trim(s.ctx, "run-1", second)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(described(meta)).To(gomega.Equal(recordstore.Meta{Stream: "run-1", Kind: Kind, Total: 3, LowSeq: 3, HighSeq: 5}))
		gomega.Expect(described(s.meta("run-1"))).To(gomega.Equal(described(meta)))
		seqs, rows := Scanned(s.backend, "run-1", 0)
		gomega.Expect(seqs).To(gomega.Equal([]int64{3, 4, 5}))
		gomega.Expect(Normalize(rows)).To(gomega.Equal(Normalize(SampleRows(3, 5))))
		seqs, _ = Scanned(s.backend, "run-1", 1)
		gomega.Expect(seqs).To(gomega.Equal([]int64{3, 4, 5}))
		seqs, _ = Scanned(s.backend, "run-1", 3)
		gomega.Expect(seqs).To(gomega.Equal([]int64{4, 5}))
	})

	ginkgo.It("trims nothing when every append is at or after the instant", func() {
		s.appendRows("run-1", SampleRows(1, 2))

		meta, err := s.backend.Trim(s.ctx, "run-1", s.harness.Now())
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(described(meta)).To(gomega.Equal(recordstore.Meta{Stream: "run-1", Kind: Kind, Total: 2, LowSeq: 1, HighSeq: 2}))
	})

	ginkgo.It("trims a stream empty and numbers the next append on from its high seq", func() {
		s.appendRows("run-1", SampleRows(1, 2))
		s.harness.Advance(appendGap)

		meta, err := s.backend.Trim(s.ctx, "run-1", s.harness.Now())
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(described(meta)).To(gomega.Equal(recordstore.Meta{Stream: "run-1", Kind: Kind, Total: 0, LowSeq: 3, HighSeq: 2}))
		seqs, _ := Scanned(s.backend, "run-1", 0)
		gomega.Expect(seqs).To(gomega.BeEmpty())

		gomega.Expect(s.appendRows("run-1", SampleRows(3, 3))).To(gomega.Equal(recordstore.Window{From: 3, To: 3}))
		gomega.Expect(described(s.meta("run-1"))).To(gomega.Equal(recordstore.Meta{Stream: "run-1", Kind: Kind, Total: 1, LowSeq: 3, HighSeq: 3}))
	})

	ginkgo.It("releases the keys of trimmed rows, so they can be appended again", func() {
		s.appendKind("run-1", KeyedKind, SampleRows(1, 2))
		s.harness.Advance(appendGap)
		second := s.harness.Now()
		s.appendKind("run-1", KeyedKind, SampleRows(3, 3))
		_, err := s.backend.Trim(s.ctx, "run-1", second)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		gomega.Expect(s.appendKind("run-1", KeyedKind, SampleRows(1, 3))).To(gomega.Equal(
			recordstore.AppendResult{Window: recordstore.Window{From: 4, To: 5}, Skipped: 1}))
		gomega.Expect(scannedNames(s.backend, "run-1")).To(gomega.Equal([]string{"row-003", "row-001", "row-002"}))
	})

	ginkgo.It("keeps a trim across a reopen", func() {
		s.appendKind("run-1", KeyedKind, SampleRows(1, 2))
		s.harness.Advance(appendGap)
		second := s.harness.Now()
		s.appendKind("run-1", KeyedKind, SampleRows(3, 4))
		_, err := s.backend.Trim(s.ctx, "run-1", second)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		s.reopen()

		gomega.Expect(described(s.meta("run-1"))).To(gomega.Equal(recordstore.Meta{Stream: "run-1", Kind: KeyedKind, Total: 2, LowSeq: 3, HighSeq: 4}))
		gomega.Expect(s.appendKind("run-1", KeyedKind, SampleRows(1, 4))).To(gomega.Equal(
			recordstore.AppendResult{Window: recordstore.Window{From: 5, To: 6}, Skipped: 2}))
	})
}

func (s *suite) retentionSpecs() {
	ginkgo.It("slides a row-retaining stream's expiry to the ttl after every append", func() {
		s.appendKind("run-1", RollingKind, SampleRows(1, 1))
		s.harness.Advance(40 * time.Minute)
		s.appendKind("run-1", RollingKind, SampleRows(2, 2))

		meta := s.meta("run-1")
		gomega.Expect(meta.ExpiresAt).ToNot(gomega.BeNil())
		gomega.Expect(*meta.ExpiresAt).To(gomega.BeTemporally("~", s.harness.Now().Add(TTL), time.Second))
		gomega.Expect(meta.Total).To(gomega.Equal(int64(2)))
	})

	ginkgo.It("slides the expiry even when every row of the append was already stored", func() {
		s.appendKind("run-1", RollingKind, SampleRows(1, 1))
		s.harness.Advance(40 * time.Minute)
		s.appendKind("run-1", RollingKind, SampleRows(1, 1))

		meta := s.meta("run-1")
		gomega.Expect(*meta.ExpiresAt).To(gomega.BeTemporally("~", s.harness.Now().Add(TTL), time.Second))
	})

	ginkgo.It("trims the rows a row-retaining stream appended longer than the ttl ago on the next append", func() {
		s.appendKind("run-1", RollingKind, SampleRows(1, 2))
		s.harness.Advance(40 * time.Minute)
		s.appendKind("run-1", RollingKind, SampleRows(3, 3))
		s.harness.Advance(30 * time.Minute)

		gomega.Expect(s.appendKind("run-1", RollingKind, SampleRows(1, 4))).To(gomega.Equal(
			recordstore.AppendResult{Window: recordstore.Window{From: 4, To: 6}, Skipped: 1}))
		gomega.Expect(described(s.meta("run-1"))).To(gomega.Equal(recordstore.Meta{Stream: "run-1", Kind: RollingKind, Total: 4, LowSeq: 3, HighSeq: 6}))
		gomega.Expect(scannedNames(s.backend, "run-1")).To(gomega.Equal([]string{"row-003", "row-001", "row-002", "row-004"}))
	})

	ginkgo.It("keeps a whole-stream kind's expiry at the ttl after its first append", func() {
		s.appendKind("run-1", KeyedKind, SampleRows(1, 1))
		first := s.meta("run-1").ExpiresAt
		gomega.Expect(first).ToNot(gomega.BeNil())
		s.harness.Advance(40 * time.Minute)
		s.appendKind("run-1", KeyedKind, SampleRows(2, 2))

		gomega.Expect(*s.meta("run-1").ExpiresAt).To(gomega.BeTemporally("~", *first, time.Second))
	})
}
