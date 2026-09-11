package sqlite_test

import (
	"context"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
)

var _ = Describe("sqlite external read lease", func() {
	It("holds index imports until the lease is released", func() {
		clock := &fakeClock{now: time.Now()}
		backend := openSQLite(filepath.Join(GinkgoT().TempDir(), "leased.sqlite"), clock, recordstoretest.Schema, true)
		DeferCleanup(backend.Close)
		Expect(backend.Table(recordstoretest.Kind)).Error().ToNot(HaveOccurred())
		source := recordstore.NewStreamMeta("run-1", recordstoretest.Kind, clock.Now())
		source.Total, source.HighSeq = 1, 1
		release := backend.Lease()
		DeferCleanup(release)
		started := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			close(started)
			_, err := backend.Import(context.Background(), recordstore.ImportRequest{
				Source: source, First: 1, Rows: recordstoretest.SampleRows(1, 1),
			})
			done <- err
		}()
		Eventually(started).Should(BeClosed())
		Consistently(done).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
		release()
		var err error
		Eventually(done).Should(Receive(&err))
		Expect(err).ToNot(HaveOccurred())
	})

	It("holds schema creation until the lease is released", func() {
		clock := &fakeClock{now: time.Now()}
		backend := openSQLite(filepath.Join(GinkgoT().TempDir(), "leased-schema.sqlite"), clock, recordstoretest.Schema, true)
		DeferCleanup(backend.Close)
		release := backend.Lease()
		DeferCleanup(release)
		started := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			close(started)
			_, err := backend.Table(recordstoretest.Kind)
			done <- err
		}()
		Eventually(started).Should(BeClosed())
		Consistently(done).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
		release()
		var err error
		Eventually(done).Should(Receive(&err))
		Expect(err).ToNot(HaveOccurred())
	})
})
