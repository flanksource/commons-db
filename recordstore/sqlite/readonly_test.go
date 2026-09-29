// Specs for a read-only backend: it reads the file another process writes,
// hands every mutation to its Submitter as a batch, and can be promoted.
package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
	"github.com/flanksource/commons-db/recordstore/sqlite"
	sqlitedb "github.com/flanksource/commons-db/sqlite"
)

var _ = Describe("sqlite backend opened read-only", func() {
	var (
		ctx       context.Context
		path      string
		clock     *fakeClock
		owner     *sqlite.Backend
		reader    *sqlite.Backend
		mu        sync.Mutex
		submitted []recordstore.Batch
		seq       atomic.Int64
	)

	submit := func(ctx context.Context, batch recordstore.Batch) (recordstore.BatchResult, error) {
		mu.Lock()
		submitted = append(submitted, batch)
		mu.Unlock()
		batch.Producer = recordstore.Producer{Instance: "reader", Seq: seq.Add(1)}
		return owner.AppendBatch(ctx, batch)
	}
	submissions := func() []recordstore.Batch {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordstore.Batch(nil), submitted...)
	}
	openReader := func(schema recordstore.SchemaResolver, submitter recordstore.Submitter) *sqlite.Backend {
		backend, err := sqlite.Open(sqlite.Options{
			Path: path, Schema: schema, TTL: recordstoretest.TTL, Now: clock.Now, SweepInterval: idleSweep,
			ReadOnly: true, Submit: submitter,
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(backend.Close)
		return backend
	}

	BeforeEach(func() {
		ctx = context.Background()
		path = filepath.Join(GinkgoT().TempDir(), "records.sqlite")
		clock = &fakeClock{now: time.Now()}
		submitted = nil
		var err error
		owner, err = sqlite.Open(sqlite.Options{
			Path: path, Schema: recordstoretest.Schema, TTL: recordstoretest.TTL, Now: clock.Now, SweepInterval: idleSweep,
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(owner.Close)
		_, err = owner.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 2))
		Expect(err).ToNot(HaveOccurred())
		reader = openReader(recordstoretest.Schema, submit)
	})

	It("refuses to open a file no writer created, or a derived index", func() {
		_, err := sqlite.Open(sqlite.Options{
			Path: filepath.Join(GinkgoT().TempDir(), "records.sqlite"), Schema: recordstoretest.Schema, SweepInterval: idleSweep, ReadOnly: true,
		})
		Expect(err).To(HaveOccurred())
		_, err = sqlite.Open(sqlite.Options{Path: path, Schema: recordstoretest.Schema, SweepInterval: idleSweep, ReadOnly: true, Derived: true})
		Expect(err).To(MatchError(ContainSubstring("derived")))
	})

	It("reads the streams the writer holds", func() {
		meta, err := reader.Meta(ctx, "run-1")
		Expect(err).ToNot(HaveOccurred())
		Expect(meta.HighSeq).To(Equal(int64(2)))
		seqs, _ := recordstoretest.Scanned(reader, "run-1", 0)
		Expect(seqs).To(Equal([]int64{1, 2}))
	})

	It("submits an append as a one-entry batch carrying its kind's schema, returning the writer's exact result", func() {
		result, err := reader.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(3, 4))
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(Equal(recordstore.AppendResult{Window: recordstore.Window{From: 3, To: 4}}))

		batches := submissions()
		Expect(batches).To(HaveLen(1))
		Expect(batches[0].ID).ToNot(BeEmpty())
		Expect(batches[0].Schemas).To(HaveLen(1))
		Expect(batches[0].Schemas[0].Kind).To(Equal(recordstoretest.Kind))
		Expect(batches[0].Entries).To(Equal([]recordstore.BatchEntry{
			{Op: recordstore.BatchAppend, Stream: "run-1", Kind: recordstoretest.Kind, Rows: recordstoretest.SampleRows(3, 4)},
		}))
		seqs, _ := recordstoretest.Scanned(owner, "run-1", 0)
		Expect(seqs).To(Equal([]int64{1, 2, 3, 4}))
	})

	It("returns the sentinel of an entry the writer refused", func() {
		Expect(reader.Seal(ctx, "run-1")).To(Succeed())
		_, err := reader.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(3, 3))
		Expect(errors.Is(err, recordstore.ErrSealed)).To(BeTrue(), "Append: %v", err)
		Expect(errors.Is(reader.Delete(ctx, "never-written"), recordstore.ErrNotFound)).To(BeTrue())
	})

	It("refuses locally what the writer would refuse, submitting nothing", func() {
		_, err := reader.Append(ctx, "run-1", recordstoretest.KeyedKind, recordstoretest.SampleRows(3, 3))
		Expect(err).To(MatchError(ContainSubstring(`holds kind`)))
		_, err = reader.Append(ctx, "run-2", recordstoretest.Kind, []recordstore.Row{{"unknown": 1}})
		Expect(err).To(MatchError(ContainSubstring(`"unknown"`)))
		Expect(submissions()).To(BeEmpty())
	})

	It("submits every other mutation", func() {
		generation := func() string {
			meta, err := reader.Meta(ctx, "run-1")
			Expect(err).ToNot(HaveOccurred())
			return meta.Generation
		}
		Expect(reader.Seal(ctx, "run-1")).To(Succeed())
		Expect(reader.Reopen(ctx, "run-1", generation())).To(Succeed())
		Expect(reader.Expire(ctx, "run-1", time.Hour)).To(Succeed())
		clock.Advance(time.Minute)
		meta, err := reader.Trim(ctx, "run-1", clock.Now())
		Expect(err).ToNot(HaveOccurred())
		Expect(meta.LowSeq).To(Equal(int64(3)))
		Expect(reader.Delete(ctx, "run-1")).To(Succeed())

		var ops []recordstore.BatchOp
		for _, batch := range submissions() {
			ops = append(ops, batch.Entries[0].Op)
		}
		Expect(ops).To(Equal([]recordstore.BatchOp{
			recordstore.BatchSeal, recordstore.BatchReopen, recordstore.BatchExpire, recordstore.BatchTrim, recordstore.BatchDelete,
		}))
		_, err = owner.Meta(ctx, "run-1")
		Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue())
	})

	It("refuses a mutation with ErrReadOnly when it has nowhere to submit it", func() {
		unsubmitting := openReader(recordstoretest.Schema, nil)
		_, err := unsubmitting.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(3, 3))
		Expect(errors.Is(err, sqlitedb.ErrReadOnly)).To(BeTrue(), "Append: %v", err)
		Expect(errors.Is(unsubmitting.Seal(ctx, "run-1"), sqlitedb.ErrReadOnly)).To(BeTrue())
		_, err = unsubmitting.Sweep(ctx)
		Expect(errors.Is(err, sqlitedb.ErrReadOnly)).To(BeTrue())
	})

	Describe("a kind's table", func() {
		It("adopts a table the writer created without writing", func() {
			table, err := reader.Table(recordstoretest.Kind)
			Expect(err).ToNot(HaveOccurred())
			Expect(table.Name).To(Equal("records_sample"))
			Expect(submissions()).To(BeEmpty())
		})

		It("has the writer declare a kind it has no table for, once, then adopts it", func() {
			table, err := reader.Table(recordstoretest.KeyedKind)
			Expect(err).ToNot(HaveOccurred())
			Expect(table.Name).To(Equal("records_keyed"))
			batches := submissions()
			Expect(batches).To(HaveLen(1))
			Expect(batches[0].Entries).To(BeEmpty())
			Expect(batches[0].Schemas).To(HaveLen(1))

			_, err = reader.Table(recordstoretest.KeyedKind)
			Expect(err).ToNot(HaveOccurred())
			Expect(submissions()).To(HaveLen(1), "the adopted table is kept")
		})

		It("has the writer add a column the kind now declares", func() {
			widened := func(kind string) (recordstore.KindSchema, error) {
				schema, err := recordstoretest.Schema(kind)
				schema.Columns = append(append([]query.ColumnDef(nil), schema.Columns...), query.ColumnDef{Name: "region", Type: query.ColumnTypeString})
				return schema, err
			}
			wider := openReader(widened, submit)
			table, err := wider.Table(recordstoretest.Kind)
			Expect(err).ToNot(HaveOccurred())
			Expect(table.Columns).To(ContainElement(HaveField("Name", "region")))
			Expect(submissions()).To(HaveLen(1))
		})

		It("has the writer build an index the kind now asks for", func() {
			timed := func(kind string) (recordstore.KindSchema, error) {
				schema, err := recordstoretest.Schema(kind)
				schema.Columns = append(append([]query.ColumnDef(nil), schema.Columns...), query.ColumnDef{Name: "at", Type: query.ColumnTypeDateTime})
				schema.Options.TimeColumn = "at"
				return schema, err
			}
			indexed := openReader(timed, submit)
			_, err := indexed.Table(recordstoretest.Kind)
			Expect(err).ToNot(HaveOccurred())
			Expect(submissions()).To(HaveLen(1))
			var name string
			Expect(readOnly(owner.Path()).QueryRowContext(ctx,
				`SELECT name FROM pragma_index_list('records_sample') WHERE origin = 'c'`).Scan(&name)).To(Succeed())
			Expect(name).To(Equal("records_sample_ix_stream_id_at_desc_seq"))
		})

		It("refuses a table the writer does not create", func() {
			ignoring := openReader(recordstoretest.Schema, func(context.Context, recordstore.Batch) (recordstore.BatchResult, error) {
				return recordstore.BatchResult{}, nil
			})
			_, err := ignoring.Table(recordstoretest.KeyedKind)
			Expect(err).To(MatchError(ContainSubstring("keyed")))
		})
	})

	It("reports every commit the writer makes as a change", func() {
		watchCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		var changes atomic.Int32
		go func() {
			defer GinkgoRecover()
			Expect(reader.WatchChanges(watchCtx, func() { changes.Add(1) })).To(Succeed())
		}()
		Consistently(changes.Load, 300*time.Millisecond).Should(BeZero())

		_, err := owner.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(3, 3))
		Expect(err).ToNot(HaveOccurred())
		Eventually(changes.Load, 2*time.Second).Should(BeNumerically(">=", 1))
	})

	It("reports its own commits as changes once it writes the file", func() {
		watchCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		var changes atomic.Int32
		go func() {
			defer GinkgoRecover()
			Expect(owner.WatchChanges(watchCtx, func() { changes.Add(1) })).To(Succeed())
		}()
		Consistently(changes.Load, 300*time.Millisecond).Should(BeZero())

		_, err := owner.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(3, 3))
		Expect(err).ToNot(HaveOccurred())
		Eventually(changes.Load, 2*time.Second).Should(BeNumerically(">=", 1))
	})

	It("names the file a configured path opens at this build's catalog version", func() {
		Expect(sqlite.CatalogVersion).To(Equal(6))
		Expect(sqlite.VersionedPath(path)).To(Equal(filepath.Join(filepath.Dir(path), "v6", "records.sqlite")))
		Expect(owner.Path()).To(Equal(sqlite.VersionedPath(path)))
	})

	It("wakes a notifier's waiter when the writer appends", func() {
		notifier, err := recordstore.NewNotifier(reader, recordstore.NotifierOptions{RecheckInterval: time.Hour})
		Expect(err).ToNot(HaveOccurred())
		meta, err := notifier.Meta(ctx, "run-1")
		Expect(err).ToNot(HaveOccurred())
		woken := make(chan recordstore.Meta, 1)
		go func() {
			defer GinkgoRecover()
			meta, err := notifier.Wait(ctx, "run-1", 2, meta.Generation)
			Expect(err).ToNot(HaveOccurred())
			woken <- meta
		}()
		Consistently(woken, 300*time.Millisecond).ShouldNot(Receive())

		_, err = owner.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(3, 3))
		Expect(err).ToNot(HaveOccurred())
		Eventually(woken, 2*time.Second).Should(Receive(HaveField("HighSeq", int64(3))))
		Expect(notifier.Close()).To(Succeed())
	})

	It("writes the file itself once promoted", func() {
		Expect(owner.Close()).To(Succeed())
		Expect(reader.Promote(ctx)).To(Succeed())

		result, err := reader.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(3, 3))
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Window).To(Equal(recordstore.Window{From: 3, To: 3}))
		Expect(submissions()).To(BeEmpty())
		_, err = reader.Sweep(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(reader.Promote(ctx)).To(Succeed(), "promoting twice changes nothing")
	})
})
