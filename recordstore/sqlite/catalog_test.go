package sqlite_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

// writeLegacy creates the file at path holding statements, as an older build
// left it.
func writeLegacy(ctx context.Context, path, statements string) {
	Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
	old, err := sql.Open("sqlite", path)
	Expect(err).ToNot(HaveOccurred())
	_, err = old.ExecContext(ctx, statements)
	Expect(err).ToNot(HaveOccurred())
	Expect(old.Close()).To(Succeed())
}

// readOnly opens the file at path without writing to it.
func readOnly(path string) *sql.DB {
	reader, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(reader.Close)
	return reader
}

// catalogVersion is the version the file at path records.
func catalogVersion(ctx context.Context, path string) int {
	var version int
	Expect(readOnly(path).QueryRowContext(ctx, `SELECT version FROM record_store_format WHERE key = 1`).Scan(&version)).To(Succeed())
	return version
}

// v3Catalog is the complete catalog of version 3, the last one storing columns
// positionally.
const v3Catalog = `CREATE TABLE record_store_format (key INTEGER PRIMARY KEY CHECK (key = 1), version INTEGER NOT NULL);
	INSERT INTO record_store_format (key, version) VALUES (1, 3);
	CREATE TABLE record_streams (
		stream_id TEXT PRIMARY KEY, generation TEXT NOT NULL, kind TEXT NOT NULL, total INTEGER NOT NULL,
		low_seq INTEGER NOT NULL, high_seq INTEGER NOT NULL,
		updated_at TEXT NOT NULL, expires_at TEXT, capped INTEGER NOT NULL DEFAULT 0, sealed INTEGER NOT NULL DEFAULT 0);
	CREATE INDEX record_streams_expires_at ON record_streams (expires_at);
	CREATE TABLE record_kinds (kind TEXT PRIMARY KEY, table_name TEXT NOT NULL, columns TEXT NOT NULL);
	CREATE TABLE record_appends (stream_id TEXT NOT NULL, last_seq INTEGER NOT NULL, appended_at TEXT NOT NULL,
		PRIMARY KEY (stream_id, last_seq));`

var _ = Describe("sqlite backend catalog versions", func() {
	var (
		ctx   context.Context
		dir   string
		clock *fakeClock
	)

	BeforeEach(func() {
		ctx = context.Background()
		dir = GinkgoT().TempDir()
		clock = &fakeClock{now: time.Now()}
	})

	It("refuses an unversioned file written in a catalog version it cannot copy", func() {
		oldPath := filepath.Join(dir, "old.sqlite")
		writeLegacy(ctx, oldPath, `CREATE TABLE record_store_format (key INTEGER PRIMARY KEY CHECK (key = 1), version INTEGER NOT NULL);
			INSERT INTO record_store_format (key, version) VALUES (1, 1)`)

		_, err := sqlite.Open(sqlite.Options{Path: oldPath, Schema: recordstoretest.Schema, SweepInterval: idleSweep})
		Expect(err).To(MatchError(ContainSubstring("unsupported catalog version 1")))
		Expect(filepath.Join(dir, "v5", "old.sqlite")).ToNot(BeAnExistingFile())
	})

	It("copies a durable version 2 file into v5, keeping its streams unsealed and sealable", func() {
		v2Path := filepath.Join(dir, "v2.sqlite")
		writeLegacy(ctx, v2Path, `CREATE TABLE record_store_format (key INTEGER PRIMARY KEY CHECK (key = 1), version INTEGER NOT NULL);
			INSERT INTO record_store_format (key, version) VALUES (1, 2);
			CREATE TABLE record_streams (
				stream_id TEXT PRIMARY KEY, generation TEXT NOT NULL, kind TEXT NOT NULL, total INTEGER NOT NULL,
				low_seq INTEGER NOT NULL, high_seq INTEGER NOT NULL,
				updated_at TEXT NOT NULL, expires_at TEXT, capped INTEGER NOT NULL DEFAULT 0);
			CREATE INDEX record_streams_expires_at ON record_streams (expires_at);
			CREATE TABLE record_kinds (kind TEXT PRIMARY KEY, table_name TEXT NOT NULL, columns TEXT NOT NULL);
			CREATE TABLE record_appends (stream_id TEXT NOT NULL, last_seq INTEGER NOT NULL, appended_at TEXT NOT NULL,
				PRIMARY KEY (stream_id, last_seq));
			INSERT INTO record_streams (stream_id, generation, kind, total, low_seq, high_seq, updated_at)
				VALUES ('kept', 'g-1', 'sample', 0, 1, 0, '2026-09-15T10:00:00.000000000Z')`)

		upgraded := openSQLite(v2Path, clock, recordstoretest.Schema, false)
		DeferCleanup(upgraded.Close)
		before, err := upgraded.Meta(ctx, "kept")
		Expect(err).ToNot(HaveOccurred())
		Expect(upgraded.Seal(ctx, "kept")).To(Succeed())
		after, err := upgraded.Meta(ctx, "kept")
		Expect(err).ToNot(HaveOccurred())
		Expect(map[string]any{
			"path": upgraded.Path(), "version": catalogVersion(ctx, upgraded.Path()), "unversioned": catalogVersion(ctx, v2Path),
			"before": before.Sealed, "after": after.Sealed, "generation": after.Generation,
		}).To(Equal(map[string]any{
			"path": filepath.Join(dir, "v5", "v2.sqlite"), "version": 5, "unversioned": 2,
			"before": false, "after": true, "generation": "g-1",
		}))
	})

	It("refuses a durable v5 file whose catalog this build cannot read", func() {
		writeLegacy(ctx, filepath.Join(dir, "v5", "records.sqlite"), `CREATE TABLE record_store_format (key INTEGER PRIMARY KEY CHECK (key = 1), version INTEGER NOT NULL);
			INSERT INTO record_store_format (key, version) VALUES (1, 5)`)

		_, err := sqlite.Open(sqlite.Options{Path: filepath.Join(dir, "records.sqlite"), Schema: recordstoretest.Schema, SweepInterval: idleSweep})
		Expect(err).To(MatchError(And(ContainSubstring("an incomplete catalog version 5"), ContainSubstring("remove the file"))))
	})

	// A derived index lives in a file a pod restart may keep while the build
	// that wrote it is replaced; everything it held can be read again from its
	// source, so its catalog is recreated rather than refused.
	DescribeTable("rebuilds a derived v5 index it cannot read, dropping everything it held",
		func(legacy string) {
			path := filepath.Join(dir, "v5", "index.sqlite")
			writeLegacy(ctx, path, legacy)

			index, err := sqlite.Open(sqlite.Options{Path: filepath.Join(dir, "index.sqlite"), Schema: recordstoretest.Schema, Derived: true, SweepInterval: idleSweep})
			Expect(err).ToNot(HaveOccurred())
			DeferCleanup(index.Close)
			source := recordstore.NewStreamMeta("run-1", recordstoretest.Kind, clock.Now())
			source.Total, source.HighSeq = 1, 1
			_, found, err := index.Prepare(ctx, source)
			Expect(err).ToNot(HaveOccurred())
			_, err = index.Import(ctx, recordstore.ImportRequest{Source: source, First: 1, Rows: recordstoretest.SampleRows(1, 1)})
			Expect(err).ToNot(HaveOccurred())
			seqs, _ := recordstoretest.Scanned(index, "run-1", 0)

			var legacyTables int
			Expect(readOnly(path).QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name = 'legacy_rows'`).Scan(&legacyTables)).To(Succeed())
			Expect(map[string]any{"version": catalogVersion(ctx, path), "legacyTables": legacyTables, "found": found, "seqs": seqs}).To(Equal(
				map[string]any{"version": 5, "legacyTables": 0, "found": false, "seqs": []int64{1}}))
		},
		Entry("an unversioned catalog", `CREATE TABLE record_streams (stream_id TEXT PRIMARY KEY, kind TEXT NOT NULL);
			CREATE TABLE legacy_rows (c0 TEXT)`),
		Entry("an older catalog version", v3Catalog+`CREATE TABLE legacy_rows (c0 TEXT)`),
		Entry("an incomplete catalog of this version", `CREATE TABLE record_store_format (key INTEGER PRIMARY KEY CHECK (key = 1), version INTEGER NOT NULL);
			INSERT INTO record_store_format (key, version) VALUES (1, 5);
			CREATE TABLE legacy_rows (c0 TEXT)`),
	)
})
