// Specs for a kind that adds a column for every key its rows bring: typed by
// the first value, kept by the file, and refused when a value disagrees.
package sqlite_test

import (
	"context"
	"encoding/json"
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

var _ = Describe("sqlite backend dynamic kinds", func() {
	var (
		ctx     context.Context
		path    string
		clock   *fakeClock
		backend *sqlite.Backend
	)

	dynamic := func(options recordstore.KindOptions) recordstore.SchemaResolver {
		options.Dynamic = true
		return func(kind string) (recordstore.KindSchema, error) {
			return recordstore.KindSchema{Kind: kind, Columns: []query.ColumnDef{{Name: "name", Type: query.ColumnTypeString}}, Options: options}, nil
		}
	}
	open := func(schema recordstore.SchemaResolver) *sqlite.Backend {
		opened := openSQLite(path, clock, schema, false)
		DeferCleanup(opened.Close)
		return opened
	}
	scanned := func(stream string) []recordstore.Row {
		_, rows := recordstoretest.Scanned(backend, stream, 0)
		return rows
	}
	tableColumns := func() []string {
		table, err := backend.Table("events")
		Expect(err).ToNot(HaveOccurred())
		var names []string
		for _, column := range table.Columns {
			names = append(names, column.Name+":"+string(column.Type))
		}
		return names
	}

	BeforeEach(func() {
		ctx = context.Background()
		path = filepath.Join(GinkgoT().TempDir(), "records.sqlite")
		clock = &fakeClock{now: time.Now()}
		backend = open(dynamic(recordstore.KindOptions{}))
	})

	It("adds a column for each key it has not seen, typed by its value, and reads it back", func() {
		_, err := backend.Append(ctx, "run-1", "events", []recordstore.Row{
			{"name": "a", "count": 3, "ok": true, "detail": map[string]any{"x": 1}, "note": "hello", "gone": nil},
		})
		Expect(err).ToNot(HaveOccurred())

		Expect(tableColumns()).To(Equal([]string{
			"stream_id:string", "seq:number", "name:string", "count:number", "detail:json", "note:string", "ok:boolean",
		}))
		rows := scanned("run-1")
		Expect(rows).To(HaveLen(1))
		Expect(rows[0]).To(And(
			HaveKeyWithValue("count", BeNumerically("==", 3)), HaveKeyWithValue("ok", true),
			HaveKeyWithValue("note", "hello"), HaveKeyWithValue("detail", map[string]any{"x": json.Number("1")}),
		))
	})

	It("never infers a datetime: a time is stored as its text", func() {
		at := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
		_, err := backend.Append(ctx, "run-1", "events", []recordstore.Row{{"name": "a", "at": at}})
		Expect(err).ToNot(HaveOccurred())
		Expect(tableColumns()).To(ContainElement("at:string"))
	})

	It("refuses a whole append whose value disagrees with a column's type, writing none of it", func() {
		_, err := backend.Append(ctx, "run-1", "events", []recordstore.Row{{"name": "a", "count": 1}})
		Expect(err).ToNot(HaveOccurred())

		_, err = backend.Append(ctx, "run-1", "events", []recordstore.Row{{"name": "b", "count": 2}, {"name": "c", "count": "three"}})
		Expect(errors.Is(err, recordstore.ErrSchemaMismatch)).To(BeTrue(), "Append: %v", err)
		_, err = backend.Append(ctx, "run-1", "events", []recordstore.Row{{"name": "d", "size": 1}, {"name": "e", "size": "big"}})
		Expect(errors.Is(err, recordstore.ErrSchemaMismatch)).To(BeTrue(), "Append: %v", err)

		Expect(scanned("run-1")).To(HaveLen(1))
		Expect(tableColumns()).ToNot(ContainElement(HavePrefix("size:")))
	})

	It("keeps the columns it inferred when the file is reopened", func() {
		_, err := backend.Append(ctx, "run-1", "events", []recordstore.Row{{"name": "a", "region": "eu"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(backend.Close()).To(Succeed())

		backend = open(dynamic(recordstore.KindOptions{}))
		Expect(scanned("run-1")[0]).To(HaveKeyWithValue("region", "eu"))
		_, err = backend.Append(ctx, "run-1", "events", []recordstore.Row{{"name": "b", "region": 3}})
		Expect(errors.Is(err, recordstore.ErrSchemaMismatch)).To(BeTrue(), "an inferred column keeps its type: %v", err)
	})

	It("refuses more inferred columns than the kind allows with ErrCapacity", func() {
		capped := open(dynamic(recordstore.KindOptions{MaxDynamicColumns: 2}))
		Expect(backend.Close()).To(Succeed())
		backend = capped
		_, err := backend.Append(ctx, "run-1", "events", []recordstore.Row{{"name": "a", "one": 1, "two": 2}})
		Expect(err).ToNot(HaveOccurred())

		_, err = backend.Append(ctx, "run-1", "events", []recordstore.Row{{"name": "b", "three": 3}})
		Expect(errors.Is(err, recordstore.ErrCapacity)).To(BeTrue(), "Append: %v", err)
		Expect(scanned("run-1")).To(HaveLen(1))
	})

	It("refuses a key a kind that is not dynamic does not declare", func() {
		declared := openSQLite(filepath.Join(GinkgoT().TempDir(), "records.sqlite"), clock, recordstoretest.Schema, false)
		DeferCleanup(declared.Close)
		_, err := declared.Append(ctx, "run-1", recordstoretest.Kind, []recordstore.Row{{"name": "a", "surprise": 1}})
		Expect(err).To(MatchError(ContainSubstring(`"surprise" is not a column`)))
	})

	It("adds the columns an imported window brings to a derived index", func() {
		index := openSQLite(filepath.Join(GinkgoT().TempDir(), "index.sqlite"), clock, dynamic(recordstore.KindOptions{}), true)
		DeferCleanup(index.Close)
		source := recordstore.NewStreamMeta("run-1", "events", clock.Now())
		source.Total, source.HighSeq = 1, 1

		_, err := index.Import(ctx, recordstore.ImportRequest{Source: source, First: 1, Rows: []recordstore.Row{{"name": "a", "region": "eu"}}})
		Expect(err).ToNot(HaveOccurred())
		_, rows := recordstoretest.Scanned(index, "run-1", 0)
		Expect(rows[0]).To(HaveKeyWithValue("region", "eu"))
	})

	It("reports a mismatched entry of a batch by its own code, committing the others", func() {
		_, err := backend.Append(ctx, "run-1", "events", []recordstore.Row{{"name": "a", "count": 1}})
		Expect(err).ToNot(HaveOccurred())

		result, err := backend.AppendBatch(ctx, recordstore.Batch{ID: "b-1", Producer: recordstore.Producer{Instance: "cli-1", Seq: 1}, Entries: []recordstore.BatchEntry{
			{Op: recordstore.BatchAppend, Stream: "run-1", Kind: "events", Rows: []recordstore.Row{{"name": "b", "count": "two"}}},
			{Op: recordstore.BatchAppend, Stream: "run-2", Kind: "events", Rows: []recordstore.Row{{"name": "c", "extra": true}}},
		}})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Entries[0].Error).ToNot(BeNil())
		Expect(result.Entries[0].Error.Code).To(Equal(recordstore.BatchErrorMismatch))
		Expect(result.Entries[1].Error).To(BeNil())
		Expect(scanned("run-2")[0]).To(HaveKeyWithValue("extra", true))
	})

	It("adopts the columns another process inferred while this one read the file", func() {
		reader, err := sqlite.Open(sqlite.Options{
			Path: path, Schema: dynamic(recordstore.KindOptions{}), Now: clock.Now, SweepInterval: idleSweep, ReadOnly: true,
			Submit: func(ctx context.Context, batch recordstore.Batch) (recordstore.BatchResult, error) {
				batch.Producer = recordstore.Producer{Instance: "reader", Seq: 1}
				return backend.AppendBatch(ctx, batch)
			},
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(reader.Close)
		_, err = backend.Append(ctx, "run-1", "events", []recordstore.Row{{"name": "a"}})
		Expect(err).ToNot(HaveOccurred())
		_, err = reader.Table("events")
		Expect(err).ToNot(HaveOccurred())

		_, err = reader.Append(ctx, "run-1", "events", []recordstore.Row{{"name": "b", "region": "eu"}})
		Expect(err).ToNot(HaveOccurred())
		_, rows := recordstoretest.Scanned(reader, "run-1", 0)
		Expect(rows[1]).To(HaveKeyWithValue("region", "eu"))
	})
})
