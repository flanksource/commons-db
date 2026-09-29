// Specs for AppendBatch: one transaction per batch with every entry isolated,
// recorded once in the ledger by batch id, and foreign schemas reconciled.
package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

var _ = Describe("sqlite backend batches", func() {
	var (
		ctx     context.Context
		path    string
		clock   *fakeClock
		backend *sqlite.Backend
	)

	producer := recordstore.Producer{Instance: "cli-1", Seq: 1, PID: 42, Host: "host", Build: "test"}

	BeforeEach(func() {
		ctx = context.Background()
		path = filepath.Join(GinkgoT().TempDir(), "records.sqlite")
		clock = &fakeClock{now: time.Now()}
		backend = openSQLite(path, clock, recordstoretest.Schema, false)
		DeferCleanup(func() { Expect(backend.Close()).To(Succeed()) })
	})

	appendEntry := func(stream string, rows ...recordstore.Row) recordstore.BatchEntry {
		return recordstore.BatchEntry{Op: recordstore.BatchAppend, Stream: stream, Kind: recordstoretest.Kind, Rows: rows}
	}
	apply := func(batch recordstore.Batch) recordstore.BatchResult {
		result, err := backend.AppendBatch(ctx, batch)
		Expect(err).ToNot(HaveOccurred())
		return result
	}
	meta := func(stream string) recordstore.Meta {
		meta, err := backend.Meta(ctx, stream)
		Expect(err).ToNot(HaveOccurred())
		return meta
	}
	names := func(stream string) []string {
		var names []string
		_, rows := recordstoretest.Scanned(backend, stream, 0)
		for _, row := range rows {
			names = append(names, row["name"].(string))
		}
		return names
	}
	countWhere := func(statement string) int {
		reader, err := sql.Open("sqlite", backend.ReadDSN())
		Expect(err).ToNot(HaveOccurred())
		defer func() { Expect(reader.Close()).To(Succeed()) }()
		var count int
		Expect(reader.QueryRowContext(ctx, statement).Scan(&count)).To(Succeed())
		return count
	}
	entryErr := func(result recordstore.EntryResult) error {
		if result.Error == nil {
			return nil
		}
		return result.Error
	}

	It("applies every entry in order and records the outcome under the batch id", func() {
		result := apply(recordstore.Batch{ID: "b-1", Producer: producer, Entries: []recordstore.BatchEntry{
			appendEntry("run-1", recordstoretest.SampleRows(1, 2)...),
			appendEntry("run-2", recordstoretest.SampleRows(1, 1)...),
			appendEntry("run-1", recordstoretest.SampleRows(3, 3)...),
			{Op: recordstore.BatchSeal, Stream: "run-1"},
		}})

		Expect(result.ID).To(Equal("b-1"))
		Expect(result.Entries).To(Equal([]recordstore.EntryResult{
			{Append: &recordstore.AppendResult{Window: recordstore.Window{From: 1, To: 2}}},
			{Append: &recordstore.AppendResult{Window: recordstore.Window{From: 1, To: 1}}},
			{Append: &recordstore.AppendResult{Window: recordstore.Window{From: 3, To: 3}}},
			{},
		}))
		Expect(names("run-1")).To(Equal([]string{"row-001", "row-002", "row-003"}))
		Expect(meta("run-1").Sealed).To(BeTrue())

		outcome, found, err := backend.BatchOutcome(ctx, "b-1")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(outcome).To(Equal(result))
	})

	It("applies a batch id once, answering a repeat with the recorded outcome", func() {
		batch := recordstore.Batch{ID: "b-1", Producer: producer, Entries: []recordstore.BatchEntry{
			appendEntry("run-1", recordstoretest.SampleRows(1, 2)...),
		}}
		first := apply(batch)

		Expect(apply(batch)).To(Equal(first))
		Expect(meta("run-1").HighSeq).To(Equal(int64(2)))
		Expect(names("run-1")).To(Equal([]string{"row-001", "row-002"}))
	})

	It("keeps the ledger across a reopen of the file", func() {
		first := apply(recordstore.Batch{ID: "b-1", Producer: producer, Entries: []recordstore.BatchEntry{
			appendEntry("run-1", recordstoretest.SampleRows(1, 1)...),
		}})
		Expect(backend.Close()).To(Succeed())
		backend = openSQLite(path, clock, recordstoretest.Schema, false)

		outcome, found, err := backend.BatchOutcome(ctx, "b-1")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(outcome).To(Equal(first))
	})

	It("reports a batch id it never applied as not found", func() {
		_, found, err := backend.BatchOutcome(ctx, "b-unknown")
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("rolls back only the entry that fails, committing the others", func() {
		apply(recordstore.Batch{ID: "b-1", Producer: producer, Entries: []recordstore.BatchEntry{
			appendEntry("sealed", recordstoretest.SampleRows(1, 1)...),
			{Op: recordstore.BatchSeal, Stream: "sealed"},
		}})

		result := apply(recordstore.Batch{ID: "b-2", Producer: producer, Entries: []recordstore.BatchEntry{
			appendEntry("run-1", recordstoretest.SampleRows(1, 1)...),
			{Op: recordstore.BatchAppend, Stream: "sealed", Kind: recordstoretest.Kind, Rows: recordstoretest.SampleRows(2, 2), Seal: true},
			appendEntry("run-1", recordstoretest.SampleRows(2, 2)...),
		}})

		Expect(entryErr(result.Entries[0])).ToNot(HaveOccurred())
		Expect(result.Entries[1].Error).ToNot(BeNil())
		Expect(result.Entries[1].Error.Code).To(Equal(recordstore.BatchErrorSealed))
		Expect(errors.Is(entryErr(result.Entries[1]), recordstore.ErrSealed)).To(BeTrue())
		Expect(entryErr(result.Entries[2])).ToNot(HaveOccurred())
		Expect(names("run-1")).To(Equal([]string{"row-001", "row-002"}))
		Expect(names("sealed")).To(Equal([]string{"row-001"}))
	})

	It("refuses an entry naming a kind other than its stream's as invalid", func() {
		apply(recordstore.Batch{ID: "b-1", Producer: producer, Entries: []recordstore.BatchEntry{
			appendEntry("run-1", recordstoretest.SampleRows(1, 1)...),
		}})

		result := apply(recordstore.Batch{ID: "b-2", Producer: producer, Entries: []recordstore.BatchEntry{
			{Op: recordstore.BatchAppend, Stream: "run-1", Kind: recordstoretest.KeyedKind, Rows: recordstoretest.SampleRows(2, 2)},
			{Op: recordstore.BatchAppend, Stream: "run-2", Kind: recordstoretest.Kind, Rows: []recordstore.Row{{"missing": 1}}},
			{Op: "compact", Stream: "run-1"},
		}})

		for _, entry := range result.Entries {
			Expect(entry.Error).ToNot(BeNil())
			Expect(entry.Error.Code).To(Equal(recordstore.BatchErrorInvalid))
		}
		Expect(meta("run-1").HighSeq).To(Equal(int64(1)))
		_, err := backend.Meta(ctx, "run-2")
		Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue())
	})

	It("starts a stream from an entry of no rows", func() {
		result := apply(recordstore.Batch{ID: "b-1", Producer: producer, Entries: []recordstore.BatchEntry{appendEntry("run-1")}})

		Expect(result.Entries[0].Append).To(Equal(&recordstore.AppendResult{Window: recordstore.Window{From: 1, To: 0}}))
		Expect(meta("run-1").Total).To(BeZero())
	})

	It("fences an entry to the generation it names", func() {
		apply(recordstore.Batch{ID: "b-1", Producer: producer, Entries: []recordstore.BatchEntry{
			appendEntry("run-1", recordstoretest.SampleRows(1, 1)...),
		}})
		generation := meta("run-1").Generation
		fenced := appendEntry("run-1", recordstoretest.SampleRows(2, 2)...)
		fenced.Generation = "an-earlier-generation"
		current := appendEntry("run-1", recordstoretest.SampleRows(3, 3)...)
		current.Generation = generation

		result := apply(recordstore.Batch{ID: "b-2", Producer: producer, Entries: []recordstore.BatchEntry{fenced, current}})

		Expect(result.Entries[0].Error).ToNot(BeNil())
		Expect(result.Entries[0].Error.Code).To(Equal(recordstore.BatchErrorNotFound))
		Expect(errors.Is(entryErr(result.Entries[0]), recordstore.ErrNotFound)).To(BeTrue())
		Expect(entryErr(result.Entries[1])).ToNot(HaveOccurred())
		Expect(names("run-1")).To(Equal([]string{"row-001", "row-003"}))
	})

	It("trims, expires, reopens and deletes streams", func() {
		apply(recordstore.Batch{ID: "b-1", Producer: producer, Entries: []recordstore.BatchEntry{
			appendEntry("trimmed", recordstoretest.SampleRows(1, 1)...),
			appendEntry("expired", recordstoretest.SampleRows(1, 1)...),
			appendEntry("reopened", recordstoretest.SampleRows(1, 1)...),
			{Op: recordstore.BatchSeal, Stream: "reopened"},
			appendEntry("deleted", recordstoretest.SampleRows(1, 1)...),
		}})
		clock.Advance(time.Minute)
		apply(recordstore.Batch{ID: "b-2", Producer: producer, Entries: []recordstore.BatchEntry{
			appendEntry("trimmed", recordstoretest.SampleRows(2, 2)...),
		}})

		result := apply(recordstore.Batch{ID: "b-3", Producer: producer, Entries: []recordstore.BatchEntry{
			{Op: recordstore.BatchTrim, Stream: "trimmed", Before: clock.Now()},
			{Op: recordstore.BatchExpire, Stream: "expired", TTL: time.Minute},
			{Op: recordstore.BatchReopen, Stream: "reopened", Generation: meta("reopened").Generation},
			appendEntry("reopened", recordstoretest.SampleRows(2, 2)...),
			{Op: recordstore.BatchDelete, Stream: "deleted"},
			{Op: recordstore.BatchDelete, Stream: "never-written"},
		}})

		Expect(result.Entries[0].Meta).ToNot(BeNil())
		Expect(result.Entries[0].Meta.LowSeq).To(Equal(int64(2)))
		Expect(names("trimmed")).To(Equal([]string{"row-002"}))
		Expect(*meta("expired").ExpiresAt).To(BeTemporally("==", clock.Now().Add(time.Minute)))
		Expect(names("reopened")).To(Equal([]string{"row-001", "row-002"}))
		Expect(meta("reopened").Sealed).To(BeFalse())
		_, err := backend.Meta(ctx, "deleted")
		Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue())
		Expect(result.Entries[5].Error).ToNot(BeNil())
		Expect(result.Entries[5].Error.Code).To(Equal(recordstore.BatchErrorNotFound))
	})

	It("adds the columns a batch's own schema declares, leaving the resolver's schema alone", func() {
		foreign := recordstore.KindSchema{
			Kind:    recordstoretest.Kind,
			Columns: append(append([]query.ColumnDef{}, recordstoretest.Columns...), query.ColumnDef{Name: "region", Type: query.ColumnTypeString}),
		}
		row := recordstoretest.SampleRow(1)
		row["region"] = "eu"

		result := apply(recordstore.Batch{ID: "b-1", Producer: producer, Schemas: []recordstore.KindSchema{foreign},
			Entries: []recordstore.BatchEntry{appendEntry("run-1", row)}})

		Expect(entryErr(result.Entries[0])).ToNot(HaveOccurred())
		table, err := backend.Table(recordstoretest.Kind)
		Expect(err).ToNot(HaveOccurred())
		Expect(table.Columns).ToNot(ContainElement(HaveField("Name", "region")))
		Expect(countWhere(`SELECT COUNT(*) FROM records_sample WHERE region = 'eu'`)).To(Equal(1))
		declared, err := recordstoretest.Schema(recordstoretest.Kind)
		Expect(err).ToNot(HaveOccurred())
		Expect(declared.Columns).To(Equal(recordstoretest.Columns))

		apply(recordstore.Batch{ID: "b-2", Producer: producer, Entries: []recordstore.BatchEntry{
			appendEntry("run-1", recordstoretest.SampleRows(2, 2)...),
		}})
		Expect(names("run-1")).To(Equal([]string{"row-001", "row-002"}))
	})

	It("refuses the entries of a batch schema that conflicts with the stored kind, committing the others", func() {
		apply(recordstore.Batch{ID: "b-1", Producer: producer, Entries: []recordstore.BatchEntry{
			appendEntry("run-1", recordstoretest.SampleRows(1, 1)...),
		}})
		retyped := recordstore.KindSchema{Kind: recordstoretest.Kind, Columns: []query.ColumnDef{
			{Name: "name", Type: query.ColumnTypeString}, {Name: "count", Type: query.ColumnTypeString},
		}}
		rekeyed := recordstore.KindSchema{Kind: recordstoretest.KeyedKind, Columns: recordstoretest.Columns}
		apply(recordstore.Batch{ID: "b-2", Producer: producer, Entries: []recordstore.BatchEntry{
			{Op: recordstore.BatchAppend, Stream: "keyed-1", Kind: recordstoretest.KeyedKind, Rows: recordstoretest.SampleRows(1, 1)},
		}})

		result := apply(recordstore.Batch{ID: "b-3", Producer: producer, Schemas: []recordstore.KindSchema{retyped, rekeyed},
			Entries: []recordstore.BatchEntry{
				appendEntry("run-1", recordstore.Row{"name": "row-002", "count": "two"}),
				{Op: recordstore.BatchAppend, Stream: "keyed-1", Kind: recordstoretest.KeyedKind, Rows: recordstoretest.SampleRows(2, 2)},
				{Op: recordstore.BatchSeal, Stream: "run-1"},
			}})

		for _, entry := range result.Entries[:2] {
			Expect(entry.Error).ToNot(BeNil())
			Expect(entry.Error.Code).To(Equal(recordstore.BatchErrorConflict))
			Expect(errors.Is(entry.Error, recordstore.ErrSchemaConflict)).To(BeTrue())
		}
		Expect(entryErr(result.Entries[2])).ToNot(HaveOccurred())
		Expect(names("run-1")).To(Equal([]string{"row-001"}))
		Expect(meta("run-1").Sealed).To(BeTrue())
	})

	It("creates the table of a kind a batch declares without appending to it", func() {
		result := apply(recordstore.Batch{ID: "b-1", Producer: producer, Schemas: []recordstore.KindSchema{
			{Kind: recordstoretest.KeyedKind, Columns: recordstoretest.Columns, Options: recordstore.KindOptions{Key: "name"}},
		}})

		Expect(result.Entries).To(BeEmpty())
		Expect(countWhere(`SELECT COUNT(*) FROM record_kinds WHERE kind = 'keyed'`)).To(Equal(1))
	})

	It("creates a kind's table for a batch without holding the batch up", func() {
		done := make(chan recordstore.BatchResult)
		go func() {
			defer GinkgoRecover()
			done <- apply(recordstore.Batch{ID: "b-1", Producer: producer, Entries: []recordstore.BatchEntry{
				appendEntry("run-1", recordstoretest.SampleRows(1, 1)...),
				{Op: recordstore.BatchAppend, Stream: "keyed-1", Kind: recordstoretest.KeyedKind, Rows: recordstoretest.SampleRows(1, 1)},
			}})
		}()

		Eventually(done, 2*time.Second).Should(Receive(HaveField("Entries", HaveLen(2))))
	})

	It("refuses a batch without an id or a producer, writing nothing", func() {
		for _, batch := range []recordstore.Batch{
			{Producer: producer, Entries: []recordstore.BatchEntry{appendEntry("run-1")}},
			{ID: "b-1", Entries: []recordstore.BatchEntry{appendEntry("run-1")}},
		} {
			_, err := backend.AppendBatch(ctx, batch)
			Expect(err).To(HaveOccurred())
		}
		_, err := backend.Meta(ctx, "run-1")
		Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue())
	})

	It("refuses a batch in a derived index, which only an Indexer fills", func() {
		index := openSQLite(filepath.Join(GinkgoT().TempDir(), "index.sqlite"), clock, recordstoretest.Schema, true)
		DeferCleanup(index.Close)

		_, err := index.AppendBatch(ctx, recordstore.Batch{ID: "b-1", Producer: producer, Entries: []recordstore.BatchEntry{appendEntry("run-1")}})
		Expect(err).To(MatchError(ContainSubstring("derived")))
	})
})
