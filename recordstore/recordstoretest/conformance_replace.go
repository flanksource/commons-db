// Conformance specs for kinds that replace stored rows: a replaced row moves to
// the next seq, so a stream's seqs may skip, and backends that cannot must refuse.
package recordstoretest

import (
	"errors"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
)

// changed is SampleRow(n) with count set to count, so a replacement is
// distinguishable from the row it replaces.
func changed(n, count int) recordstore.Row {
	row := SampleRow(n)
	row["count"] = count
	return row
}

func (s *suite) replaceSpecs() {
	ginkgo.Context("a kind that replaces stored rows", func() {
		ginkgo.BeforeEach(func() {
			if !s.harness.Replace {
				ginkgo.Skip("the backend does not store kinds that replace rows")
			}
		})

		ginkgo.It("removes the stored row and stores the appended one at the next seq", func() {
			s.appendKind("run-1", ReplacingKind, SampleRows(1, 3))

			gomega.Expect(s.appendKind("run-1", ReplacingKind, []recordstore.Row{changed(2, 99), SampleRow(4)})).To(gomega.Equal(
				recordstore.AppendResult{Window: recordstore.Window{From: 4, To: 5}, Replaced: 1}))
			seqs, rows := Scanned(s.backend, "run-1", 0)
			gomega.Expect(seqs).To(gomega.Equal([]int64{1, 3, 4, 5}))
			gomega.Expect(Normalize(rows)).To(gomega.Equal(Normalize([]recordstore.Row{SampleRow(1), SampleRow(3), changed(2, 99), SampleRow(4)})))
			gomega.Expect(described(s.meta("run-1"))).To(gomega.Equal(
				recordstore.Meta{Stream: "run-1", Kind: ReplacingKind, Total: 4, LowSeq: 1, HighSeq: 5}))
		})

		ginkgo.It("keeps the low seq as a lower bound when the lowest row is replaced", func() {
			s.appendKind("run-1", ReplacingKind, SampleRows(1, 2))
			s.appendKind("run-1", ReplacingKind, []recordstore.Row{changed(1, 99)})

			seqs, _ := Scanned(s.backend, "run-1", 0)
			gomega.Expect(seqs).To(gomega.Equal([]int64{2, 3}))
			gomega.Expect(described(s.meta("run-1"))).To(gomega.Equal(
				recordstore.Meta{Stream: "run-1", Kind: ReplacingKind, Total: 2, LowSeq: 1, HighSeq: 3}))
		})

		ginkgo.It("trims around the seqs replaced rows left behind, counting only the rows held", func() {
			s.appendKind("run-1", ReplacingKind, SampleRows(1, 3))
			s.harness.Advance(appendGap)
			second := s.harness.Now()
			s.appendKind("run-1", ReplacingKind, []recordstore.Row{changed(1, 99)})

			meta, err := s.backend.Trim(s.ctx, "run-1", second)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(described(meta)).To(gomega.Equal(
				recordstore.Meta{Stream: "run-1", Kind: ReplacingKind, Total: 1, LowSeq: 4, HighSeq: 4}))
			gomega.Expect(scannedNames(s.backend, "run-1")).To(gomega.Equal([]string{"row-001"}))
		})

		ginkgo.It("renews a row-retaining stream's row when it is replaced", func() {
			s.appendKind("run-1", RollingReplacingKind, SampleRows(1, 2))
			s.harness.Advance(40 * time.Minute)
			s.appendKind("run-1", RollingReplacingKind, []recordstore.Row{changed(1, 99)})
			s.harness.Advance(30 * time.Minute)
			s.appendKind("run-1", RollingReplacingKind, SampleRows(3, 3))

			gomega.Expect(scannedNames(s.backend, "run-1")).To(gomega.Equal([]string{"row-001", "row-003"}))
			gomega.Expect(s.meta("run-1").Total).To(gomega.Equal(int64(2)))
		})

		ginkgo.It("keeps replacing after the backend is reopened", func() {
			s.appendKind("run-1", ReplacingKind, SampleRows(1, 2))
			s.reopen()

			gomega.Expect(s.appendKind("run-1", ReplacingKind, []recordstore.Row{changed(2, 99)})).To(gomega.Equal(
				recordstore.AppendResult{Window: recordstore.Window{From: 3, To: 3}, Replaced: 1}))
			gomega.Expect(s.meta("run-1").Total).To(gomega.Equal(int64(2)))
		})
	})

	ginkgo.It("refuses a kind that replaces stored rows when it cannot store one, writing nothing", func() {
		if s.harness.Replace {
			ginkgo.Skip("the backend stores kinds that replace rows")
		}
		_, err := s.backend.Append(s.ctx, "run-1", ReplacingKind, SampleRows(1, 1))
		gomega.Expect(errors.Is(err, recordstore.ErrUnsupported)).To(gomega.BeTrue(), "Append: %v", err)
		_, err = s.backend.Meta(s.ctx, "run-1")
		gomega.Expect(errors.Is(err, recordstore.ErrNotFound)).To(gomega.BeTrue(), "Meta: %v", err)
	})
}
