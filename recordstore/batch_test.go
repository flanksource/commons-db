// Specs for the batch types: what a batch entry's error unwraps to, and a
// Notifier waking every stream a batch wrote.
package recordstore_test

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

var _ = Describe("BatchError", func() {
	DescribeTable("unwraps to the sentinel its code names",
		func(code recordstore.BatchErrorCode, sentinel error) {
			err := &recordstore.BatchError{Code: code, Message: "entry 2"}
			Expect(errors.Is(err, sentinel)).To(BeTrue())
			Expect(err.Error()).To(Equal("entry 2"))
		},
		Entry("sealed", recordstore.BatchErrorSealed, recordstore.ErrSealed),
		Entry("not found", recordstore.BatchErrorNotFound, recordstore.ErrNotFound),
		Entry("capacity", recordstore.BatchErrorCapacity, recordstore.ErrCapacity),
		Entry("conflict", recordstore.BatchErrorConflict, recordstore.ErrSchemaConflict),
		Entry("unsupported", recordstore.BatchErrorUnsupported, recordstore.ErrUnsupported),
	)

	It("unwraps an invalid entry to no sentinel", func() {
		err := &recordstore.BatchError{Code: recordstore.BatchErrorInvalid, Message: "bad row"}
		for _, sentinel := range []error{recordstore.ErrSealed, recordstore.ErrNotFound, recordstore.ErrCapacity,
			recordstore.ErrSchemaConflict, recordstore.ErrUnsupported} {
			Expect(errors.Is(err, sentinel)).To(BeFalse())
		}
	})

	DescribeTable("codes an entry's error by the sentinel it wraps",
		func(err error, code recordstore.BatchErrorCode) {
			Expect(recordstore.NewBatchError(err).Code).To(Equal(code))
			Expect(recordstore.NewBatchError(err).Message).To(Equal(err.Error()))
		},
		Entry("sealed", errors.Join(errors.New("stream run-1"), recordstore.ErrSealed), recordstore.BatchErrorSealed),
		Entry("not found", errors.Join(errors.New("stream run-1"), recordstore.ErrNotFound), recordstore.BatchErrorNotFound),
		Entry("conflict", errors.Join(errors.New("kind sample"), recordstore.ErrSchemaConflict), recordstore.BatchErrorConflict),
		Entry("anything else", errors.New("row 1 key \"x\" is not a column"), recordstore.BatchErrorInvalid),
	)
})

var _ = Describe("Notifier batches", func() {
	It("wakes a waiter on every stream a batch through it wrote", func() {
		ctx := context.Background()
		backend, err := sqlite.Open(sqlite.Options{
			Path: filepath.Join(GinkgoT().TempDir(), "records.sqlite"), Schema: recordstoretest.Schema, SweepInterval: time.Hour,
		})
		Expect(err).ToNot(HaveOccurred())
		notifier, err := recordstore.NewNotifier(backend, recordstore.NotifierOptions{RecheckInterval: time.Hour})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(notifier.Close)
		Expect(notifier.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 1))).Error().ToNot(HaveOccurred())
		meta, err := notifier.Meta(ctx, "run-1")
		Expect(err).ToNot(HaveOccurred())

		woken := make(chan recordstore.Meta, 1)
		go func() {
			defer GinkgoRecover()
			meta, err := notifier.Wait(ctx, "run-1", 1, meta.Generation)
			Expect(err).ToNot(HaveOccurred())
			woken <- meta
		}()
		Consistently(woken, 100*time.Millisecond).ShouldNot(Receive())

		_, err = notifier.AppendBatch(ctx, recordstore.Batch{
			ID: "b-1", Producer: recordstore.Producer{Instance: "cli-1", Seq: 1},
			Entries: []recordstore.BatchEntry{{Op: recordstore.BatchAppend, Stream: "run-1", Kind: recordstoretest.Kind, Rows: recordstoretest.SampleRows(2, 2)}},
		})
		Expect(err).ToNot(HaveOccurred())
		Eventually(woken, time.Second).Should(Receive(HaveField("HighSeq", int64(2))))

		outcome, found, err := notifier.BatchOutcome(ctx, "b-1")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(outcome.ID).To(Equal("b-1"))
	})

	It("refuses a batch when the backend it wraps cannot take one", func() {
		notifier, err := recordstore.NewNotifier(openMemoryKV(), recordstore.NotifierOptions{RecheckInterval: time.Hour})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(notifier.Close)

		_, err = notifier.AppendBatch(context.Background(), recordstore.Batch{ID: "b-1", Producer: recordstore.Producer{Instance: "cli-1"}})
		Expect(errors.Is(err, recordstore.ErrUnsupported)).To(BeTrue())
	})
})
