package recordstore_test

import (
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
)

var _ = Describe("Notifier", func() {
	// A recheck no spec lives to see, so only an append can wake a waiter.
	const neverRechecked = time.Hour

	var (
		ctx      context.Context
		notifier *recordstore.Notifier
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		notifier, err = recordstore.NewNotifier(openMemoryKV(), recordstore.NotifierOptions{RecheckInterval: neverRechecked})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(notifier.Close)
		Expect(notifier.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 2))).Error().ToNot(HaveOccurred())
	})

	generation := func() string {
		meta, err := notifier.Meta(ctx, "run-1")
		Expect(err).ToNot(HaveOccurred())
		return meta.Generation
	}

	It("wakes a waiter as soon as an append through it commits, without waiting for a recheck", func() {
		woken := make(chan recordstore.Meta, 1)
		read := generation()
		go func() {
			defer GinkgoRecover()
			meta, err := notifier.Wait(ctx, "run-1", 2, read)
			Expect(err).ToNot(HaveOccurred())
			woken <- meta
		}()
		Consistently(woken, 100*time.Millisecond).ShouldNot(Receive())

		Expect(notifier.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(3, 3))).Error().ToNot(HaveOccurred())
		Eventually(woken, time.Second).Should(Receive(HaveField("HighSeq", int64(3))))
	})

	It("wakes a waiter as soon as a seal through it commits, returning the sealed metadata", func() {
		woken := make(chan recordstore.Meta, 1)
		read := generation()
		go func() {
			defer GinkgoRecover()
			meta, err := notifier.Wait(ctx, "run-1", 2, read)
			Expect(err).ToNot(HaveOccurred())
			woken <- meta
		}()
		Consistently(woken, 100*time.Millisecond).ShouldNot(Receive())

		Expect(notifier.Seal(ctx, "run-1")).To(Succeed())
		Eventually(woken, time.Second).Should(Receive(And(HaveField("Sealed", true), HaveField("HighSeq", int64(2)))))
	})

	It("returns at once when the stream already holds a row after the seq", func() {
		meta, err := notifier.Wait(ctx, "run-1", 1, generation())
		Expect(err).ToNot(HaveOccurred())
		Expect(meta.HighSeq).To(Equal(int64(2)))
	})

	It("reports a generation the stream no longer has as recordstore.ErrNotFound", func() {
		_, err := notifier.Wait(ctx, "run-1", 2, "an-older-generation")
		Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue(), "Wait: %v", err)
	})

	It("returns the context's error when the wait is cancelled", func() {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, err := notifier.Wait(cancelled, "run-1", 2, generation())
		Expect(errors.Is(err, context.Canceled)).To(BeTrue(), "Wait: %v", err)
	})

	DescribeTable("refuses options it cannot run with",
		func(backend recordstore.Backend, options recordstore.NotifierOptions, message string) {
			_, err := recordstore.NewNotifier(backend, options)
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("no backend", nil, recordstore.NotifierOptions{RecheckInterval: time.Second}, "backend is required"),
		Entry("no recheck interval", openMemoryKV(), recordstore.NotifierOptions{}, "RecheckInterval"),
	)

	It("refuses to wrap a notifier, whose appends would wake only one of the two", func() {
		_, err := recordstore.NewNotifier(notifier, recordstore.NotifierOptions{RecheckInterval: time.Second})
		Expect(err).To(MatchError(ContainSubstring("already is a notifier")))
	})
})
