package recordstoretest

import (
	"errors"
	"fmt"
	"sync"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
)

// scannedNames is the name of every row the stream holds, in seq order.
func scannedNames(backend recordstore.Backend, stream string) []string {
	_, rows := Scanned(backend, stream, 0)
	names := make([]string, len(rows))
	for index, row := range rows {
		names[index] = fmt.Sprint(row["name"])
	}
	return names
}

// sampleNames are the names of SampleRows(first, last).
func sampleNames(first, last int) []string {
	names := make([]string, 0, last-first+1)
	for _, row := range SampleRows(first, last) {
		names = append(names, row["name"].(string))
	}
	return names
}

func (s *suite) keySpecs() {
	ginkgo.It("skips every row of a keyed append whose key the stream already holds", func() {
		gomega.Expect(s.appendKind("run-1", KeyedKind, SampleRows(1, 3))).To(gomega.Equal(
			recordstore.AppendResult{Window: recordstore.Window{From: 1, To: 3}}))

		gomega.Expect(s.appendKind("run-1", KeyedKind, SampleRows(1, 3))).To(gomega.Equal(
			recordstore.AppendResult{Window: recordstore.Window{From: 4, To: 3}, Skipped: 3}))
		seqs, rows := Scanned(s.backend, "run-1", 0)
		gomega.Expect(seqs).To(gomega.Equal([]int64{1, 2, 3}))
		gomega.Expect(Normalize(rows)).To(gomega.Equal(Normalize(SampleRows(1, 3))))
		gomega.Expect(s.meta("run-1").Total).To(gomega.Equal(int64(3)))
	})

	ginkgo.It("appends only the new rows of a keyed append that overlaps the stream, numbered contiguously", func() {
		s.appendKind("run-1", KeyedKind, SampleRows(1, 3))

		gomega.Expect(s.appendKind("run-1", KeyedKind, SampleRows(2, 6))).To(gomega.Equal(
			recordstore.AppendResult{Window: recordstore.Window{From: 4, To: 6}, Skipped: 2}))
		seqs, rows := Scanned(s.backend, "run-1", 0)
		gomega.Expect(seqs).To(gomega.Equal([]int64{1, 2, 3, 4, 5, 6}))
		gomega.Expect(Normalize(rows)).To(gomega.Equal(Normalize(SampleRows(1, 6))))
	})

	ginkgo.It("stores every row of an unkeyed kind, however often it is appended", func() {
		s.appendKind("run-1", Kind, SampleRows(1, 3))

		gomega.Expect(s.appendKind("run-1", Kind, SampleRows(1, 3))).To(gomega.Equal(
			recordstore.AppendResult{Window: recordstore.Window{From: 4, To: 6}}))
		gomega.Expect(scannedNames(s.backend, "run-1")).To(gomega.Equal(append(sampleNames(1, 3), sampleNames(1, 3)...)))
	})

	ginkgo.It("refuses a keyed append naming one key twice, writing none of it", func() {
		rows := append(SampleRows(1, 2), SampleRow(1))
		_, err := s.backend.Append(s.ctx, "run-1", KeyedKind, rows)
		gomega.Expect(err).To(gomega.MatchError(gomega.And(gomega.ContainSubstring(`"row-001"`), gomega.ContainSubstring("once"))))
		_, err = s.backend.Meta(s.ctx, "run-1")
		gomega.Expect(errors.Is(err, recordstore.ErrNotFound)).To(gomega.BeTrue(), "Meta: %v", err)
	})

	ginkgo.It("refuses a keyed row without a non-empty string key", func() {
		for _, name := range []any{nil, "", 7} {
			row := SampleRow(1)
			row["name"] = name
			_, err := s.backend.Append(s.ctx, "run-1", KeyedKind, []recordstore.Row{row})
			gomega.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("non-empty string")), "name %#v", name)
		}
	})

	ginkgo.It("stores each key once when overlapping keyed appends race", func() {
		const writers = 8
		var wg sync.WaitGroup
		for writer := range writers {
			wg.Add(1)
			go func() {
				defer ginkgo.GinkgoRecover()
				defer wg.Done()
				_, err := s.backend.Append(s.ctx, "run-1", KeyedKind, SampleRows(writer+1, writer+4))
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
			}()
		}
		wg.Wait()

		seqs, _ := Scanned(s.backend, "run-1", 0)
		gomega.Expect(seqs).To(gomega.HaveLen(writers + 3))
		for index, seq := range seqs {
			gomega.Expect(seq).To(gomega.Equal(int64(index + 1)))
		}
		gomega.Expect(scannedNames(s.backend, "run-1")).To(gomega.ConsistOf(sampleNames(1, writers+3)))
	})

	ginkgo.It("still holds a keyed stream's keys after the backend is reopened", func() {
		s.appendKind("run-1", KeyedKind, SampleRows(1, 3))
		s.reopen()

		gomega.Expect(s.appendKind("run-1", KeyedKind, SampleRows(1, 4))).To(gomega.Equal(
			recordstore.AppendResult{Window: recordstore.Window{From: 4, To: 4}, Skipped: 3}))
		gomega.Expect(scannedNames(s.backend, "run-1")).To(gomega.Equal(sampleNames(1, 4)))
	})
}
