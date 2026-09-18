package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

func TestSQLite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Record Store SQLite Suite")
}

// fakeClock is the backend's clock, started at the wall clock so an expiry it
// stamps reads correctly against time.Now in the shared suite. It is read by
// the backend's sweeper goroutine as well as the spec, so it is locked, and it
// counts its reads so a spec can tell that the sweeper stopped reading it.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	reads int
}

type scanOutcome struct {
	seqs []int64
	err  error
}

type sweepOutcome struct {
	count int
	err   error
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeClock) Reads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

// idleSweep is a sweep interval no spec waits out: every spec that is not
// about the sweeper sweeps by hand.
const idleSweep = time.Hour

func openSQLite(path string, clock *fakeClock, schema recordstore.SchemaResolver, derived bool) *sqlite.Backend {
	backend, err := sqlite.Open(sqlite.Options{Path: path, Schema: schema, Now: clock.Now, Derived: derived, SweepInterval: idleSweep})
	Expect(err).ToNot(HaveOccurred())
	return backend
}

// columnsSchema resolves every kind to columns, unkeyed.
func columnsSchema(columns ...query.ColumnDef) recordstore.SchemaResolver {
	return func(kind string) (recordstore.KindSchema, error) {
		return recordstore.KindSchema{Kind: kind, Columns: columns}, nil
	}
}

var _ = Describe("sqlite backend", func() {
	recordstoretest.Conformance(func() recordstoretest.Harness {
		clock := &fakeClock{now: time.Now()}
		path := filepath.Join(GinkgoT().TempDir(), "records.sqlite")
		open := func() recordstore.Backend {
			backend, err := sqlite.Open(sqlite.Options{
				Path: path, Schema: recordstoretest.Schema, TTL: recordstoretest.TTL, Now: clock.Now, SweepInterval: idleSweep,
			})
			Expect(err).ToNot(HaveOccurred())
			return backend
		}
		backend := open().(*sqlite.Backend)
		return recordstoretest.Harness{
			Backend: backend, Advance: clock.Advance, Now: clock.Now, Reopen: open,
			Elapse: func(d time.Duration) {
				clock.Advance(d)
				_, err := backend.Sweep(context.Background())
				Expect(err).ToNot(HaveOccurred())
			},
		}
	})
})

var _ = Describe("sqlite backend storage", func() {
	var (
		ctx     context.Context
		path    string
		clock   *fakeClock
		backend *sqlite.Backend
	)

	BeforeEach(func() {
		ctx = context.Background()
		path = filepath.Join(GinkgoT().TempDir(), "records.sqlite")
		clock = &fakeClock{now: time.Now()}
		backend = openSQLite(path, clock, recordstoretest.Schema, false)
		DeferCleanup(func() { Expect(backend.Close()).To(Succeed()) })
	})

	countRows := func() int {
		reader, err := sql.Open("sqlite", backend.ReadDSN())
		Expect(err).ToNot(HaveOccurred())
		defer func() { Expect(reader.Close()).To(Succeed()) }()
		var count int
		Expect(reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM "records_sample"`).Scan(&count)).To(Succeed())
		return count
	}

	It("imports rows under the seqs another backend gave them, and refuses a gap", func() {
		source := recordstore.NewStreamMeta("run-1", recordstoretest.Kind, clock.Now())
		source.Total, source.HighSeq = 2, 2
		window, err := backend.Import(ctx, recordstore.ImportRequest{Source: source, First: 1, Rows: recordstoretest.SampleRows(1, 2)})
		Expect(err).ToNot(HaveOccurred())
		Expect(window).To(Equal(recordstore.Window{From: 1, To: 2}))

		_, err = backend.Import(ctx, recordstore.ImportRequest{Source: source, First: 5, Rows: recordstoretest.SampleRows(5, 5)})
		Expect(err).To(MatchError(ContainSubstring("the next seq is 3")))
		seqs, _ := recordstoretest.Scanned(backend, "run-1", 0)
		Expect(seqs).To(Equal([]int64{1, 2}))
	})

	It("sweeps the rows of an expired stream out of the file and leaves live ones", func() {
		_, err := backend.Append(ctx, "old", recordstoretest.Kind, recordstoretest.SampleRows(1, 3))
		Expect(err).ToNot(HaveOccurred())
		_, err = backend.Append(ctx, "live", recordstoretest.Kind, recordstoretest.SampleRows(1, 2))
		Expect(err).ToNot(HaveOccurred())
		Expect(backend.Expire(ctx, "old", time.Minute)).To(Succeed())

		clock.Advance(2 * time.Minute)
		swept, err := backend.Sweep(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(swept).To(Equal(1))
		Expect(countRows()).To(Equal(2))
	})

	// Nothing may have swept the stream yet, and its rows still hold the seqs
	// a fresh stream numbers from.
	It("starts an expired stream again at seq 1 when it is appended to before any sweep", func() {
		_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 3))
		Expect(err).ToNot(HaveOccurred())
		Expect(backend.Expire(ctx, "run-1", time.Minute)).To(Succeed())
		clock.Advance(2 * time.Minute)

		result, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(10, 11))
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(Equal(recordstore.AppendResult{Window: recordstore.Window{From: 1, To: 2}}))
		seqs, rows := recordstoretest.Scanned(backend, "run-1", 0)
		Expect(seqs).To(Equal([]int64{1, 2}))
		Expect(recordstoretest.Normalize(rows)).To(Equal(recordstoretest.Normalize(recordstoretest.SampleRows(10, 11))))
		Expect(countRows()).To(Equal(2))
	})

	It("starts an expired stream at seq 1 when expiry crosses the append commit boundary", func() {
		_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 2))
		Expect(err).ToNot(HaveOccurred())
		Expect(backend.Expire(ctx, "run-1", time.Minute)).To(Succeed())

		release := backend.Lease()
		DeferCleanup(release)
		done := make(chan error, 1)
		go func() {
			_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(10, 10))
			done <- err
		}()
		Consistently(done).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
		clock.Advance(2 * time.Minute)
		release()

		var appendErr error
		Eventually(done).Should(Receive(&appendErr))
		Expect(appendErr).ToNot(HaveOccurred())
		seqs, rows := recordstoretest.Scanned(backend, "run-1", 0)
		Expect(seqs).To(Equal([]int64{1}))
		Expect(recordstoretest.Normalize(rows)).To(Equal(recordstoretest.Normalize(recordstoretest.SampleRows(10, 10))))
	})

	It("escapes URI metacharacters in the database path", func() {
		escapedPath := filepath.Join(GinkgoT().TempDir(), "records?#.sqlite")
		escaped := openSQLite(escapedPath, clock, recordstoretest.Schema, false)
		DeferCleanup(escaped.Close)
		_, err := escaped.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 1))
		Expect(err).ToNot(HaveOccurred())
		_, rows := recordstoretest.Scanned(escaped, "run-1", 0)
		Expect(recordstoretest.Normalize(rows)).To(Equal(recordstoretest.Normalize(recordstoretest.SampleRows(1, 1))))
		Expect(escaped.Path()).To(Equal(filepath.Join(filepath.Dir(escapedPath), "v5", "records?#.sqlite")))
		Expect(os.Stat(escaped.Path())).Error().ToNot(HaveOccurred())
	})

	It("sweeps expired streams on its own every sweep interval, until it is closed", func() {
		sweepClock := &fakeClock{now: time.Now()}
		swept, err := sqlite.Open(sqlite.Options{
			Path: filepath.Join(GinkgoT().TempDir(), "swept.sqlite"), Schema: recordstoretest.Schema,
			Now: sweepClock.Now, SweepInterval: 10 * time.Millisecond,
		})
		Expect(err).ToNot(HaveOccurred())
		_, err = swept.Append(ctx, "old", recordstoretest.Kind, recordstoretest.SampleRows(1, 3))
		Expect(err).ToNot(HaveOccurred())
		Expect(swept.Expire(ctx, "old", time.Minute)).To(Succeed())
		sweepClock.Advance(2 * time.Minute)

		reader, err := sql.Open("sqlite", swept.ReadDSN())
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(reader.Close)
		Eventually(func() (int, error) {
			var count int
			err := reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM "records_sample"`).Scan(&count)
			return count, err
		}).WithTimeout(2 * time.Second).Should(BeZero())

		Expect(swept.Close()).To(Succeed())
		reads := sweepClock.Reads()
		time.Sleep(50 * time.Millisecond)
		Expect(sweepClock.Reads()).To(Equal(reads), "the sweeper read the clock after Close returned")
	})

	It("refuses to open without a sweep interval, which would keep every expired stream forever", func() {
		_, err := sqlite.Open(sqlite.Options{Path: filepath.Join(GinkgoT().TempDir(), "x.sqlite"), Schema: recordstoretest.Schema})
		Expect(err).To(MatchError(ContainSubstring("sweep interval")))
	})

	It("rejects an unversioned legacy catalog when opening", func() {
		legacyPath := filepath.Join(GinkgoT().TempDir(), "legacy.sqlite")
		legacy, err := sql.Open("sqlite", legacyPath)
		Expect(err).ToNot(HaveOccurred())
		_, err = legacy.ExecContext(ctx, `CREATE TABLE record_streams (stream_id TEXT PRIMARY KEY, kind TEXT NOT NULL)`)
		Expect(err).ToNot(HaveOccurred())
		Expect(legacy.Close()).To(Succeed())

		_, err = sqlite.Open(sqlite.Options{
			Path: legacyPath, Schema: recordstoretest.Schema, SweepInterval: idleSweep,
		})
		Expect(err).To(MatchError(ContainSubstring("unsupported unversioned")))
	})

	It("is readable through its read-only DSN while it is being written", func() {
		_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 4))
		Expect(err).ToNot(HaveOccurred())
		Expect(countRows()).To(Equal(4))
	})

	It("opens profile connections in query-only mode", func() {
		reader, err := sql.Open("sqlite", backend.ReadDSN())
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(reader.Close)
		var queryOnly int
		Expect(reader.QueryRowContext(ctx, `PRAGMA query_only`).Scan(&queryOnly)).To(Succeed())
		Expect(queryOnly).To(Equal(1))
	})

	It("keeps an external multi-statement read stable for the lifetime of its lease", func() {
		const pageSize = 1000
		_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, pageSize+1))
		Expect(err).ToNot(HaveOccurred())
		reader, err := sql.Open("sqlite", backend.ReadDSN())
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(reader.Close)
		readPage := func(offset int) []int64 {
			rows, err := reader.QueryContext(ctx, `SELECT seq FROM records_sample WHERE stream_id = ? ORDER BY seq LIMIT ? OFFSET ?`, "run-1", pageSize, offset)
			Expect(err).ToNot(HaveOccurred())
			defer func() { Expect(rows.Close()).To(Succeed()) }()
			var seqs []int64
			for rows.Next() {
				var seq int64
				Expect(rows.Scan(&seq)).To(Succeed())
				seqs = append(seqs, seq)
			}
			Expect(rows.Err()).ToNot(HaveOccurred())
			return seqs
		}

		release := backend.Lease()
		DeferCleanup(release)
		Expect(len(readPage(0))).To(Equal(pageSize))
		appendStarted := make(chan struct{})
		appendDone := make(chan error, 1)
		go func() {
			close(appendStarted)
			_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(pageSize+2, pageSize+2))
			appendDone <- err
		}()
		Eventually(appendStarted).Should(BeClosed())
		Consistently(appendDone).WithTimeout(100 * time.Millisecond).ShouldNot(Receive())
		Expect(readPage(pageSize)).To(Equal([]int64{pageSize + 1}))

		release()
		var appendErr error
		Eventually(appendDone).Should(Receive(&appendErr))
		Expect(appendErr).ToNot(HaveOccurred())
		Expect(readPage(pageSize)).To(Equal([]int64{pageSize + 1, pageSize + 2}))
	})

	It("finishes a scan snapshot while Sweep removes the expired stream", func() {
		const rowsAcrossTwoPages = 1001
		_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, rowsAcrossTwoPages))
		Expect(err).ToNot(HaveOccurred())
		Expect(backend.Expire(ctx, "run-1", time.Minute)).To(Succeed())

		callbackStarted := make(chan struct{})
		resumeCallback := make(chan struct{})
		var callbackOnce sync.Once
		var resumeOnce sync.Once
		resumeScan := func() { resumeOnce.Do(func() { close(resumeCallback) }) }
		DeferCleanup(resumeScan)
		scanDone := make(chan scanOutcome, 1)
		go func() {
			var seqs []int64
			err := backend.Scan(ctx, "run-1", 0, func(seq int64, _ recordstore.Row) error {
				seqs = append(seqs, seq)
				callbackOnce.Do(func() {
					close(callbackStarted)
					<-resumeCallback
				})
				return nil
			})
			scanDone <- scanOutcome{seqs: seqs, err: err}
		}()
		Eventually(callbackStarted).Should(BeClosed())

		clock.Advance(2 * time.Minute)
		sweepDone := make(chan sweepOutcome, 1)
		go func() {
			count, err := backend.Sweep(ctx)
			sweepDone <- sweepOutcome{count: count, err: err}
		}()
		var swept sweepOutcome
		Eventually(sweepDone).Should(Receive(&swept))
		Expect(swept.err).ToNot(HaveOccurred())
		Expect(swept.count).To(Equal(1))

		resumeScan()
		var scanned scanOutcome
		Eventually(scanDone).Should(Receive(&scanned))
		Expect(scanned.err).ToNot(HaveOccurred())
		Expect(len(scanned.seqs)).To(Equal(rowsAcrossTwoPages))
		Expect(scanned.seqs[0]).To(Equal(int64(1)))
		Expect(scanned.seqs[rowsAcrossTwoPages-1]).To(Equal(int64(rowsAcrossTwoPages)))
		_, err = backend.Meta(ctx, "run-1")
		Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue(), fmt.Sprint(err))
	})

	It("does not include rows appended after its scan snapshot starts", func() {
		const rowsInFirstPage = 1000
		_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, rowsInFirstPage))
		Expect(err).ToNot(HaveOccurred())

		callbackStarted := make(chan struct{})
		resumeCallback := make(chan struct{})
		var callbackOnce sync.Once
		var resumeOnce sync.Once
		resumeScan := func() { resumeOnce.Do(func() { close(resumeCallback) }) }
		DeferCleanup(resumeScan)
		scanDone := make(chan scanOutcome, 1)
		go func() {
			var seqs []int64
			err := backend.Scan(ctx, "run-1", 0, func(seq int64, _ recordstore.Row) error {
				seqs = append(seqs, seq)
				callbackOnce.Do(func() {
					close(callbackStarted)
					<-resumeCallback
				})
				return nil
			})
			scanDone <- scanOutcome{seqs: seqs, err: err}
		}()
		Eventually(callbackStarted).Should(BeClosed())

		appendDone := make(chan error, 1)
		go func() {
			_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(rowsInFirstPage+1, rowsInFirstPage+1))
			appendDone <- err
		}()
		var appendErr error
		Eventually(appendDone).Should(Receive(&appendErr))
		Expect(appendErr).ToNot(HaveOccurred())

		resumeScan()
		var scanned scanOutcome
		Eventually(scanDone).Should(Receive(&scanned))
		Expect(scanned.err).ToNot(HaveOccurred())
		Expect(len(scanned.seqs)).To(Equal(rowsInFirstPage))
		Expect(scanned.seqs[rowsInFirstPage-1]).To(Equal(int64(rowsInFirstPage)))
		seqs, _ := recordstoretest.Scanned(backend, "run-1", 0)
		Expect(seqs).To(HaveLen(rowsInFirstPage + 1))
	})

	It("lets a scan callback read and write the backend", func() {
		_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 2))
		Expect(err).ToNot(HaveOccurred())
		scanCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		scanDone := make(chan error, 1)
		go func() {
			scanDone <- backend.Scan(scanCtx, "run-1", 0, func(seq int64, _ recordstore.Row) error {
				if seq != 1 {
					return nil
				}
				if _, err := backend.Meta(scanCtx, "run-1"); err != nil {
					return err
				}
				_, err := backend.Append(scanCtx, "run-2", recordstoretest.Kind, recordstoretest.SampleRows(10, 10))
				return err
			})
		}()
		var scanErr error
		Eventually(scanDone).Should(Receive(&scanErr))
		Expect(scanErr).ToNot(HaveOccurred())
		meta, err := backend.Meta(ctx, "run-2")
		Expect(err).ToNot(HaveOccurred())
		Expect(meta.Stream).To(Equal("run-2"))
		Expect(meta.Kind).To(Equal(recordstoretest.Kind))
		Expect(meta.Total).To(Equal(int64(1)))
		Expect(meta.HighSeq).To(Equal(int64(1)))
	})

	It("rejects overlapping, gapped and stale-generation imports", func() {
		source := recordstore.NewStreamMeta("run-1", recordstoretest.Kind, clock.Now())
		source.Total, source.HighSeq = 5, 5
		_, err := backend.Import(ctx, recordstore.ImportRequest{Source: source, First: 1, Rows: recordstoretest.SampleRows(1, 3)})
		Expect(err).ToNot(HaveOccurred())

		_, err = backend.Import(ctx, recordstore.ImportRequest{Source: source, First: 1, Rows: recordstoretest.SampleRows(1, 1)})
		Expect(err).To(MatchError(ContainSubstring("the next seq is 4")))
		_, err = backend.Import(ctx, recordstore.ImportRequest{Source: source, First: 5, Rows: recordstoretest.SampleRows(5, 5)})
		Expect(err).To(MatchError(ContainSubstring("the next seq is 4")))

		stale := source
		stale.Generation = "stale-generation"
		_, err = backend.Import(ctx, recordstore.ImportRequest{Source: stale, First: 4, Rows: recordstoretest.SampleRows(4, 4)})
		Expect(err).To(MatchError(ContainSubstring("not import generation")))
		seqs, _ := recordstoretest.Scanned(backend, "run-1", 0)
		Expect(seqs).To(Equal([]int64{1, 2, 3}))
	})

	It("prepares a derived index by removing a stale generation", func() {
		derived := openSQLite(filepath.Join(GinkgoT().TempDir(), "derived.sqlite"), clock, recordstoretest.Schema, true)
		DeferCleanup(derived.Close)
		oldSource := recordstore.NewStreamMeta("run-1", recordstoretest.Kind, clock.Now())
		oldSource.Total, oldSource.HighSeq = 3, 3
		_, err := derived.Import(ctx, recordstore.ImportRequest{Source: oldSource, First: 1, Rows: recordstoretest.SampleRows(1, 3)})
		Expect(err).ToNot(HaveOccurred())

		newSource := recordstore.NewStreamMeta("run-1", recordstoretest.Kind, clock.Now())
		newSource.Total, newSource.HighSeq = 2, 2
		indexed, found, err := derived.Prepare(ctx, newSource)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
		Expect(indexed).To(BeZero())
		_, err = derived.Meta(ctx, "run-1")
		Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue(), fmt.Sprint(err))

		_, err = derived.Import(ctx, recordstore.ImportRequest{Source: newSource, First: 1, Rows: recordstoretest.SampleRows(10, 11)})
		Expect(err).ToNot(HaveOccurred())
		_, err = derived.Import(ctx, recordstore.ImportRequest{Source: oldSource, First: 3, Rows: recordstoretest.SampleRows(3, 3)})
		Expect(err).To(MatchError(ContainSubstring("not import generation")))
	})

	It("refuses a row key its kind does not declare", func() {
		_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, []recordstore.Row{{"name": "x", "colour": "red"}})
		Expect(err).To(MatchError(ContainSubstring(`"colour"`)))
	})

	It("refuses a kind retaining rows when it keeps streams without a ttl", func() {
		_, err := backend.Append(ctx, "run-1", recordstoretest.RollingKind, recordstoretest.SampleRows(1, 1))
		Expect(err).To(MatchError(ContainSubstring("retains rows")))
		_, err = backend.Meta(ctx, "run-1")
		Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue(), fmt.Sprint(err))
	})

	It("refuses a kind its schema resolver does not know", func() {
		_, err := backend.Append(ctx, "run-1", "unknown", recordstoretest.SampleRows(1, 1))
		Expect(err).To(MatchError(ContainSubstring(`kind "unknown"`)))
	})

	It("stores datetimes in one fixed-width UTC form, so they sort as instants", func() {
		schema := columnsSchema(query.ColumnDef{Name: "at", Type: query.ColumnTypeDateTime})
		timed := openSQLite(filepath.Join(GinkgoT().TempDir(), "timed.sqlite"), clock, schema, false)
		DeferCleanup(timed.Close)
		zone := time.FixedZone("SAST", 2*3600)
		_, err := timed.Append(ctx, "run-1", "timed", []recordstore.Row{
			{"at": time.Date(2026, 9, 10, 8, 0, 5, 500_000_000, zone)},
			{"at": "2026-09-10T06:00:05Z"},
			{"at": time.Date(2026, 9, 10, 6, 0, 5, 120_000_000, time.UTC)},
		})
		Expect(err).ToNot(HaveOccurred())

		_, rows := recordstoretest.Scanned(timed, "run-1", 0)
		Expect(rows).To(Equal([]recordstore.Row{
			{"at": "2026-09-10T06:00:05.500000000Z"},
			{"at": "2026-09-10T06:00:05.000000000Z"},
			{"at": "2026-09-10T06:00:05.120000000Z"},
		}))
		_, err = timed.Append(ctx, "run-1", "timed", []recordstore.Row{{"at": "yesterday"}})
		Expect(err).To(MatchError(ContainSubstring("RFC3339")))
	})

	Context("when a kind's columns change between processes", func() {
		gainedColumn := query.ColumnDef{Name: "object type", Type: query.ColumnTypeString}
		wider := columnsSchema(append(slices.Clone(recordstoretest.Columns), gainedColumn)...)
		var catalogBefore string

		BeforeEach(func() {
			_, err := backend.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(1, 2))
			Expect(err).ToNot(HaveOccurred())
			Expect(backend.Close()).To(Succeed())
			catalogBefore = kindColumns(ctx, backend.Path(), recordstoretest.Kind)
		})

		// rowsWith is SampleRows first..last, each carrying the gained column.
		rowsWith := func(first, last int, objectType any) []recordstore.Row {
			rows := recordstoretest.SampleRows(first, last)
			for _, row := range rows {
				row["object type"] = objectType
			}
			return rows
		}

		DescribeTable("adds the column the kind gained to its table in place, keeping every stream it held",
			func(derived bool) {
				reopened := openSQLite(path, clock, wider, derived)
				backend = reopened
				_, rows := recordstoretest.Scanned(reopened, "run-1", 0)
				Expect(map[string]any{
					"rows":     recordstoretest.Normalize(rows),
					"catalog":  kindColumns(ctx, reopened.Path(), recordstoretest.Kind),
					"physical": tableColumns(ctx, reopened.Path(), "records_sample"),
				}).To(Equal(map[string]any{
					"rows":     recordstoretest.Normalize(rowsWith(1, 2, nil)),
					"catalog":  strings.TrimSuffix(catalogBefore, "]") + `,"object type=object_type:TEXT:string"]`,
					"physical": []string{"stream_id", "seq", "name", "count", "ok", "detail", "object_type"},
				}))
			},
			Entry("in a durable file", false),
			Entry("in a derived index", true),
		)

		// Two builds share one file: each reads and writes the columns it
		// declares, and neither loses the other's.
		It("reads and writes a table wider than a narrower declaration of its kind, keeping the columns it does not declare", func() {
			wide := openSQLite(path, clock, wider, false)
			_, err := wide.Append(ctx, "run-1", recordstoretest.Kind, rowsWith(3, 3, "USRTAB"))
			Expect(err).ToNot(HaveOccurred())
			Expect(wide.Close()).To(Succeed())

			narrow := openSQLite(path, clock, recordstoretest.Schema, false)
			_, err = narrow.Append(ctx, "run-1", recordstoretest.Kind, recordstoretest.SampleRows(4, 4))
			Expect(err).ToNot(HaveOccurred())
			_, narrowRows := recordstoretest.Scanned(narrow, "run-1", 0)
			Expect(narrow.Close()).To(Succeed())

			backend = openSQLite(path, clock, wider, false)
			_, wideRows := recordstoretest.Scanned(backend, "run-1", 0)
			Expect(map[string]any{
				"narrow": recordstoretest.Normalize(narrowRows), "wide": recordstoretest.Normalize(wideRows),
				"catalog": kindColumns(ctx, backend.Path(), recordstoretest.Kind),
			}).To(Equal(map[string]any{
				"narrow":  recordstoretest.Normalize(recordstoretest.SampleRows(1, 4)),
				"wide":    recordstoretest.Normalize(slices.Concat(rowsWith(1, 2, nil), rowsWith(3, 3, "USRTAB"), rowsWith(4, 4, nil))),
				"catalog": strings.TrimSuffix(catalogBefore, "]") + `,"object type=object_type:TEXT:string"]`,
			}))
		})

		Context("and one of them changes type", func() {
			retyped := columnsSchema(query.ColumnDef{Name: "name", Type: query.ColumnTypeString}, query.ColumnDef{Name: "count", Type: query.ColumnTypeString},
				query.ColumnDef{Name: "ok", Type: query.ColumnTypeBoolean}, query.ColumnDef{Name: "detail", Type: query.ColumnTypeJSON})

			It("refuses to reinterpret a durable table's rows under the new type", func() {
				reopened := openSQLite(path, clock, retyped, false)
				backend = reopened
				_, err := reopened.Append(ctx, "run-2", recordstoretest.Kind, []recordstore.Row{{"name": "x", "count": "one"}})
				Expect(err).To(MatchError(And(ContainSubstring(`column "count"`), ContainSubstring("only added columns"))))
				Expect(kindColumns(ctx, reopened.Path(), recordstoretest.Kind)).To(Equal(catalogBefore))
			})

			It("rebuilds a derived index's table, dropping the streams it held", func() {
				reopened := openSQLite(path, clock, retyped, true)
				backend = reopened
				source := recordstore.NewStreamMeta("run-2", recordstoretest.Kind, clock.Now())
				source.Total, source.HighSeq = 1, 1
				_, found, err := reopened.Prepare(ctx, source)
				Expect(err).ToNot(HaveOccurred())
				Expect(found).To(BeFalse())

				_, err = reopened.Meta(ctx, "run-1")
				Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue(), fmt.Sprint(err))
				_, err = reopened.Import(ctx, recordstore.ImportRequest{Source: source, First: 1, Rows: []recordstore.Row{{"name": "x", "count": "one"}}})
				Expect(err).ToNot(HaveOccurred())
			})
		})
	})
})
