// Specs for a kind's indexes: the time index that serves newest-first pages,
// declared indexes, and a file keeping every index any build declared.
package sqlite_test

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

var _ = Describe("sqlite backend indexes", func() {
	var (
		ctx   context.Context
		path  string
		clock *fakeClock
	)

	timedColumns := []query.ColumnDef{
		{Name: "name", Type: query.ColumnTypeString},
		{Name: "at", Type: query.ColumnTypeDateTime},
		{Name: "region", Type: query.ColumnTypeString},
	}
	schema := func(options recordstore.KindOptions) recordstore.SchemaResolver {
		return func(kind string) (recordstore.KindSchema, error) {
			return recordstore.KindSchema{Kind: kind, Columns: timedColumns, Options: options}, nil
		}
	}
	open := func(options recordstore.KindOptions, derived bool) *sqlite.Backend {
		backend := openSQLite(path, clock, schema(options), derived)
		DeferCleanup(backend.Close)
		if derived {
			return backend
		}
		_, err := backend.Append(ctx, "run-1", "timed", []recordstore.Row{{"name": "a", "at": clock.Now(), "region": "eu"}})
		Expect(err).ToNot(HaveOccurred())
		return backend
	}
	indexNames := func(backend *sqlite.Backend) []string {
		rows, err := readOnly(backend.Path()).QueryContext(ctx, `SELECT name FROM pragma_index_list('records_timed') WHERE origin = 'c' ORDER BY name`)
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

	BeforeEach(func() {
		ctx = context.Background()
		path = filepath.Join(GinkgoT().TempDir(), "records.sqlite")
		clock = &fakeClock{now: time.Now()}
	})

	It("indexes a kind's rows by stream and time, newest first", func() {
		backend := open(recordstore.KindOptions{TimeColumn: "at"}, false)

		Expect(indexNames(backend)).To(Equal([]string{"records_timed_ix_stream_id_at_desc_seq"}))
	})

	It("pages a stream newest first without sorting it", func() {
		backend := open(recordstore.KindOptions{TimeColumn: "at"}, false)

		rows, err := readOnly(backend.Path()).QueryContext(ctx,
			`EXPLAIN QUERY PLAN SELECT * FROM records_timed WHERE stream_id = ? ORDER BY at DESC, seq LIMIT 100`, "run-1")
		Expect(err).ToNot(HaveOccurred())
		defer func() { Expect(rows.Close()).To(Succeed()) }()
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			Expect(rows.Scan(&id, &parent, &unused, &detail)).To(Succeed())
			plan = append(plan, detail)
		}
		Expect(strings.Join(plan, "\n")).To(And(
			ContainSubstring("records_timed_ix_stream_id_at_desc_seq"), Not(ContainSubstring("TEMP B-TREE"))))
	})

	It("builds the indexes a kind declares, each after the stream", func() {
		backend := open(recordstore.KindOptions{Indexes: []recordstore.IndexDef{{Columns: []string{"region", "name"}}}}, false)

		Expect(indexNames(backend)).To(Equal([]string{"records_timed_ix_stream_id_region_name"}))
	})

	It("keeps an index a later build no longer declares, and adds the one it now declares", func() {
		first := open(recordstore.KindOptions{TimeColumn: "at"}, false)
		Expect(first.Close()).To(Succeed())

		second := open(recordstore.KindOptions{Indexes: []recordstore.IndexDef{{Columns: []string{"region"}}}}, false)
		Expect(indexNames(second)).To(Equal([]string{"records_timed_ix_stream_id_at_desc_seq", "records_timed_ix_stream_id_region"}))
		Expect(second.Close()).To(Succeed())

		third := open(recordstore.KindOptions{}, false)
		Expect(indexNames(third)).To(Equal([]string{"records_timed_ix_stream_id_at_desc_seq", "records_timed_ix_stream_id_region"}))
		result, err := third.Append(ctx, "run-1", "timed", []recordstore.Row{{"name": "b", "at": clock.Now()}})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Window).To(Equal(recordstore.Window{From: 4, To: 4}), "each open appended one row before this one")
	})

	It("keeps every index while adding a column", func() {
		first := open(recordstore.KindOptions{TimeColumn: "at"}, false)
		Expect(first.Close()).To(Succeed())
		timedColumns = append(timedColumns, query.ColumnDef{Name: "extra", Type: query.ColumnTypeString})
		DeferCleanup(func() { timedColumns = timedColumns[:3] })

		second := open(recordstore.KindOptions{TimeColumn: "at"}, false)
		Expect(indexNames(second)).To(Equal([]string{"records_timed_ix_stream_id_at_desc_seq"}))
	})

	It("indexes a derived index's rows the same way", func() {
		index := open(recordstore.KindOptions{TimeColumn: "at"}, true)
		source := recordstore.NewStreamMeta("run-1", "timed", clock.Now())
		source.Total, source.HighSeq = 1, 1
		_, err := index.Import(ctx, recordstore.ImportRequest{Source: source, First: 1, Rows: []recordstore.Row{{"name": "a", "at": clock.Now()}}})
		Expect(err).ToNot(HaveOccurred())

		Expect(indexNames(index)).To(Equal([]string{"records_timed_ix_stream_id_at_desc_seq"}))
	})
})
