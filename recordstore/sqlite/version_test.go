package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

// eventsSchema resolves every kind to columns whose declared names are not all
// safe SQLite identifiers, keyed on id.
func eventsSchema(kind string) (recordstore.KindSchema, error) {
	return recordstore.KindSchema{Kind: kind, Options: recordstore.KindOptions{Key: "id"}, Columns: []query.ColumnDef{
		{Name: "id", Type: query.ColumnTypeString},
		{Name: "Pod Name", Type: query.ColumnTypeString},
		{Name: "transaction", Type: query.ColumnTypeString},
		{Name: "PolicyGuid", Type: query.ColumnTypeString},
		{Name: "policyGuid", Type: query.ColumnTypeString},
		{Name: "count", Type: query.ColumnTypeNumber},
	}}, nil
}

// eventRow is the n-th row of the events kind.
func eventRow(n int) recordstore.Row {
	return recordstore.Row{
		"id": fmt.Sprintf("e-%d", n), "Pod Name": fmt.Sprintf("pod-%d", n), "transaction": fmt.Sprintf("tx-%d", n),
		"PolicyGuid": fmt.Sprintf("P-%d", n), "policyGuid": fmt.Sprintf("p-%d", n), "count": int64(n),
	}
}

// v3Events is a durable version 3 file holding stream run-1 of kind events,
// rows e-1 and e-2 at seqs 1 and 2, stored positionally as that version did.
const v3Events = v3Catalog + `PRAGMA journal_mode = WAL;
	INSERT INTO record_kinds (kind, table_name, columns) VALUES ('events', 'records_events',
		'["stream_id:TEXT:string","seq:NUMERIC:number","id:TEXT:string","Pod Name:TEXT:string","transaction:TEXT:string","PolicyGuid:TEXT:string","policyGuid:TEXT:string","count:NUMERIC:number","key:id"]');
	CREATE TABLE "records_events" ("c0" TEXT, "c1" NUMERIC, "c2" TEXT, "c3" TEXT, "c4" TEXT, "c5" TEXT, "c6" TEXT, "c7" NUMERIC, PRIMARY KEY ("c0", "c1"));
	CREATE UNIQUE INDEX "records_events_key" ON "records_events" ("c0", "c2");
	INSERT INTO "records_events" VALUES ('run-1', 1, 'e-1', 'pod-1', 'tx-1', 'P-1', 'p-1', 1), ('run-1', 2, 'e-2', 'pod-2', 'tx-2', 'P-2', 'p-2', 2);
	INSERT INTO record_streams (stream_id, generation, kind, total, low_seq, high_seq, updated_at)
		VALUES ('run-1', 'g-1', 'events', 2, 1, 2, '2026-09-15T10:00:00.000000000Z');
	INSERT INTO record_appends (stream_id, last_seq, appended_at) VALUES ('run-1', 2, '2026-09-15T10:00:00.000000000Z');`

// namedEventsColumns is the events kind's catalog entry once its columns are
// named.
const namedEventsColumns = `["stream_id=stream_id:TEXT:string","seq=seq:NUMERIC:number","id=id:TEXT:string","Pod Name=Pod_Name:TEXT:string","transaction=transaction_:TEXT:string","PolicyGuid=PolicyGuid:TEXT:string","policyGuid=policyGuid_2:TEXT:string","count=count:NUMERIC:number","key:id"]`

// v4Events is a durable version 4 file holding stream run-1 of kind events,
// rows e-1 and e-2 at seqs 1 and 2, under named columns.
const v4Events = `CREATE TABLE record_store_format (key INTEGER PRIMARY KEY CHECK (key = 1), version INTEGER NOT NULL);
	INSERT INTO record_store_format (key, version) VALUES (1, 4);
	CREATE TABLE record_streams (
		stream_id TEXT PRIMARY KEY, generation TEXT NOT NULL, kind TEXT NOT NULL, total INTEGER NOT NULL,
		low_seq INTEGER NOT NULL, high_seq INTEGER NOT NULL,
		updated_at TEXT NOT NULL, expires_at TEXT, capped INTEGER NOT NULL DEFAULT 0, sealed INTEGER NOT NULL DEFAULT 0);
	CREATE INDEX record_streams_expires_at ON record_streams (expires_at);
	CREATE TABLE record_kinds (kind TEXT PRIMARY KEY, table_name TEXT NOT NULL, columns TEXT NOT NULL);
	CREATE TABLE record_appends (stream_id TEXT NOT NULL, last_seq INTEGER NOT NULL, appended_at TEXT NOT NULL,
		PRIMARY KEY (stream_id, last_seq));
	PRAGMA journal_mode = WAL;
	INSERT INTO record_kinds (kind, table_name, columns) VALUES ('events', 'records_events', '` + namedEventsColumns + `');
	CREATE TABLE "records_events" ("stream_id" TEXT, "seq" NUMERIC, "id" TEXT, "Pod_Name" TEXT, "transaction_" TEXT, "PolicyGuid" TEXT, "policyGuid_2" TEXT, "count" NUMERIC, PRIMARY KEY ("stream_id", "seq"));
	CREATE UNIQUE INDEX "records_events_key" ON "records_events" ("stream_id", "id");
	INSERT INTO "records_events" VALUES ('run-1', 1, 'e-1', 'pod-1', 'tx-1', 'P-1', 'p-1', 1), ('run-1', 2, 'e-2', 'pod-2', 'tx-2', 'P-2', 'p-2', 2);
	INSERT INTO record_streams (stream_id, generation, kind, total, low_seq, high_seq, updated_at)
		VALUES ('run-1', 'g-1', 'events', 2, 1, 2, '2026-09-15T10:00:00.000000000Z');
	INSERT INTO record_appends (stream_id, last_seq, appended_at) VALUES ('run-1', 2, '2026-09-15T10:00:00.000000000Z');`

func fileBytes(path string) []byte {
	content, err := os.ReadFile(path)
	Expect(err).ToNot(HaveOccurred())
	return content
}

func tableColumns(ctx context.Context, path, table string) []string {
	rows, err := readOnly(path).QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	Expect(err).ToNot(HaveOccurred())
	defer func() { Expect(rows.Close()).To(Succeed()) }()
	var names []string
	for rows.Next() {
		var name string
		Expect(rows.Scan(&name)).To(Succeed())
		names = append(names, name)
	}
	Expect(rows.Err()).ToNot(HaveOccurred())
	return names
}

func kindColumns(ctx context.Context, path, kind string) string {
	var columns string
	Expect(readOnly(path).QueryRowContext(ctx, `SELECT columns FROM record_kinds WHERE kind = ?`, kind).Scan(&columns)).To(Succeed())
	return columns
}

var _ = Describe("sqlite backend versioned files", func() {
	var (
		ctx         context.Context
		dir         string
		unversioned string
		clock       *fakeClock
	)

	BeforeEach(func() {
		ctx = context.Background()
		dir = GinkgoT().TempDir()
		unversioned = filepath.Join(dir, "records.sqlite")
		clock = &fakeClock{now: time.Date(2026, 9, 15, 11, 0, 0, 0, time.UTC)}
	})

	It("opens a new file under the catalog version's directory", func() {
		backend := openSQLite(unversioned, clock, eventsSchema, false)
		DeferCleanup(backend.Close)
		_, err := backend.Append(ctx, "run-1", "events", []recordstore.Row{eventRow(1)})
		Expect(err).ToNot(HaveOccurred())

		Expect(backend.Path()).To(Equal(filepath.Join(dir, "v5", "records.sqlite")))
		Expect(unversioned).ToNot(BeAnExistingFile())
		Expect(map[string]any{
			"version": catalogVersion(ctx, backend.Path()), "columns": kindColumns(ctx, backend.Path(), "events"),
			"physical": tableColumns(ctx, backend.Path(), "records_events"),
		}).To(Equal(map[string]any{
			"version": 5, "columns": namedEventsColumns,
			"physical": []string{"stream_id", "seq", "id", "Pod_Name", "transaction_", "PolicyGuid", "policyGuid_2", "count"},
		}))
	})

	Context("when a durable version 4 file holds rows", func() {
		var v4Path string
		var original []byte

		BeforeEach(func() {
			v4Path = filepath.Join(dir, "v4", "records.sqlite")
			writeLegacy(ctx, v4Path, v4Events)
			// An older unversioned file is superseded by the v4 copy made of it.
			writeLegacy(ctx, unversioned, v3Catalog)
			original = fileBytes(v4Path)
		})

		It("copies it into v5 as it is, leaving the v4 file for the builds still reading it", func() {
			backend := openSQLite(unversioned, clock, eventsSchema, false)
			DeferCleanup(backend.Close)

			seqs, rows := scannedEvents(backend)
			Expect(map[string]any{
				"path": backend.Path(), "seqs": seqs, "rows": rows,
				"version": catalogVersion(ctx, backend.Path()), "columns": kindColumns(ctx, backend.Path(), "events"),
			}).To(Equal(map[string]any{
				"path": filepath.Join(dir, "v5", "records.sqlite"), "seqs": []int64{1, 2},
				"rows":    []recordstore.Row{eventRow(1), eventRow(2)},
				"version": 5, "columns": namedEventsColumns,
			}))
			Expect(backend.Close()).To(Succeed())
			Expect(fileBytes(v4Path)).To(Equal(original), "the v4 file changed")
		})

		It("refuses a v4 file whose catalog is incomplete rather than copying it", func() {
			writeLegacy(ctx, v4Path, `DROP TABLE record_appends`)
			_, err := sqlite.Open(sqlite.Options{Path: unversioned, Schema: eventsSchema, SweepInterval: idleSweep})
			Expect(err).To(MatchError(And(ContainSubstring("an incomplete catalog version 4"), ContainSubstring("cannot be copied"))))
		})
	})

	Context("when a durable version 3 file holds rows", func() {
		var original []byte

		BeforeEach(func() {
			writeLegacy(ctx, unversioned, v3Events)
			original = fileBytes(unversioned)
		})

		It("copies it into v5 with named columns, the same rows and working key skips, leaving the original untouched", func() {
			backend := openSQLite(unversioned, clock, eventsSchema, false)
			DeferCleanup(backend.Close)

			seqs, rows := scannedEvents(backend)
			result, err := backend.Append(ctx, "run-1", "events", []recordstore.Row{eventRow(2), eventRow(3)})
			Expect(err).ToNot(HaveOccurred())
			appendedSeqs, _ := scannedEvents(backend)
			Expect(map[string]any{
				"seqs": seqs, "rows": rows, "append": result, "after": appendedSeqs,
				"version": catalogVersion(ctx, backend.Path()), "columns": kindColumns(ctx, backend.Path(), "events"),
				"physical": tableColumns(ctx, backend.Path(), "records_events"),
			}).To(Equal(map[string]any{
				"seqs": []int64{1, 2}, "rows": []recordstore.Row{eventRow(1), eventRow(2)},
				"append":  recordstore.AppendResult{Window: recordstore.Window{From: 3, To: 3}, Skipped: 1},
				"after":   []int64{1, 2, 3},
				"version": 5, "columns": namedEventsColumns,
				"physical": []string{"stream_id", "seq", "id", "Pod_Name", "transaction_", "PolicyGuid", "policyGuid_2", "count"},
			}))
			Expect(backend.Close()).To(Succeed())
			Expect(fileBytes(unversioned)).To(Equal(original), "the unversioned file changed")
		})

		It("reuses the v5 copy on a second open rather than copying again", func() {
			first := openSQLite(unversioned, clock, eventsSchema, false)
			_, err := first.Append(ctx, "run-1", "events", []recordstore.Row{eventRow(3)})
			Expect(err).ToNot(HaveOccurred())
			Expect(first.Close()).To(Succeed())
			// An older build keeps writing the unversioned file; the copy must not see it.
			writeLegacy(ctx, unversioned, `INSERT INTO record_streams (stream_id, generation, kind, total, low_seq, high_seq, updated_at)
				VALUES ('old-build', 'g-2', 'events', 0, 1, 0, '2026-09-15T10:30:00.000000000Z')`)

			second := openSQLite(unversioned, clock, eventsSchema, false)
			DeferCleanup(second.Close)
			seqs, _ := scannedEvents(second)
			_, err = second.Meta(ctx, "old-build")
			Expect(map[string]any{"seqs": seqs, "oldBuildStreamFound": !errors.Is(err, recordstore.ErrNotFound)}).To(Equal(
				map[string]any{"seqs": []int64{1, 2, 3}, "oldBuildStreamFound": false}))
		})

		It("builds a derived index fresh in v5, copying nothing", func() {
			index := openSQLite(unversioned, clock, eventsSchema, true)
			DeferCleanup(index.Close)

			_, err := index.Meta(ctx, "run-1")
			Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue(), fmt.Sprint(err))
			Expect(index.Path()).To(Equal(filepath.Join(dir, "v5", "records.sqlite")))
			Expect(catalogVersion(ctx, index.Path())).To(Equal(5))
			Expect(index.Close()).To(Succeed())
			Expect(fileBytes(unversioned)).To(Equal(original), "the unversioned file changed")
		})
	})

	// The physical names are the table's own once it exists: a build deriving
	// them differently must read the table through the names it was stored with.
	It("reads a table through its stored physical names when they differ from a fresh derivation", func() {
		first := openSQLite(unversioned, clock, eventsSchema, false)
		_, err := first.Append(ctx, "run-1", "events", []recordstore.Row{eventRow(1), eventRow(2)})
		Expect(err).ToNot(HaveOccurred())
		path := first.Path()
		Expect(first.Close()).To(Succeed())
		writeLegacy(ctx, path, `ALTER TABLE "records_events" RENAME COLUMN "Pod_Name" TO "pod_named_elsewhere";
			UPDATE record_kinds SET columns = replace(columns, 'Pod Name=Pod_Name:', 'Pod Name=pod_named_elsewhere:')`)

		second := openSQLite(unversioned, clock, eventsSchema, false)
		DeferCleanup(second.Close)
		result, err := second.Append(ctx, "run-1", "events", []recordstore.Row{eventRow(2), eventRow(3)})
		Expect(err).ToNot(HaveOccurred())
		seqs, rows := scannedEvents(second)
		Expect(map[string]any{"append": result, "seqs": seqs, "rows": rows}).To(Equal(map[string]any{
			"append": recordstore.AppendResult{Window: recordstore.Window{From: 3, To: 3}, Skipped: 1},
			"seqs":   []int64{1, 2, 3}, "rows": []recordstore.Row{eventRow(1), eventRow(2), eventRow(3)},
		}))
	})

	It("refuses a durable table whose catalog names a column the table does not have", func() {
		first := openSQLite(unversioned, clock, eventsSchema, false)
		_, err := first.Append(ctx, "run-1", "events", []recordstore.Row{eventRow(1)})
		Expect(err).ToNot(HaveOccurred())
		path := first.Path()
		Expect(first.Close()).To(Succeed())
		writeLegacy(ctx, path, `ALTER TABLE "records_events" RENAME COLUMN "Pod_Name" TO "renamed_behind_its_back"`)

		second := openSQLite(unversioned, clock, eventsSchema, false)
		DeferCleanup(second.Close)
		_, err = second.Append(ctx, "run-1", "events", []recordstore.Row{eventRow(2)})
		Expect(err).To(MatchError(ContainSubstring("renamed_behind_its_back")))
	})
})

func scannedEvents(backend *sqlite.Backend) ([]int64, []recordstore.Row) {
	var seqs []int64
	var rows []recordstore.Row
	Expect(backend.Scan(context.Background(), "run-1", 0, func(seq int64, row recordstore.Row) error {
		seqs = append(seqs, seq)
		rows = append(rows, row)
		return nil
	})).To(Succeed())
	return seqs, rows
}
