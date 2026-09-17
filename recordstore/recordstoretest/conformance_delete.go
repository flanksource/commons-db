package recordstoretest

import (
	"errors"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func (s *suite) deleteSpecs() {
	ginkgo.It("deletes only the named stream and permits a new generation under its id", func() {
		s.appendKind("run-1", KeyedKind, SampleRows(1, 2))
		s.appendRows("run-2", SampleRows(3, 3))
		original := s.meta("run-1")

		gomega.Expect(s.backend.Delete(s.ctx, "run-1")).To(gomega.Succeed())
		_, err := s.backend.Meta(s.ctx, "run-1")
		gomega.Expect(errors.Is(err, recordstore.ErrNotFound)).To(gomega.BeTrue(), "%v", err)
		_, rows := Scanned(s.backend, "run-2", 0)
		gomega.Expect(Normalize(rows)).To(gomega.Equal(Normalize(SampleRows(3, 3))))
		s.reopen()
		gomega.Expect(s.appendKind("run-1", KeyedKind, SampleRows(1, 1)).Window).To(gomega.Equal(recordstore.Window{From: 1, To: 1}))
		gomega.Expect(s.meta("run-1").Generation).ToNot(gomega.Equal(original.Generation))
	})

	ginkgo.It("reports a missing stream when deleting it", func() {
		err := s.backend.Delete(s.ctx, "missing")
		gomega.Expect(errors.Is(err, recordstore.ErrNotFound)).To(gomega.BeTrue(), "%v", err)
	})
}
