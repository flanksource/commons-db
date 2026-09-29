// Specs for compaction: dropping the rows a kind's rules select from the
// middle of its streams, and merging streams into a new one.
package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/recordstoretest"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

var _ = Describe("sqlite backend compaction", func() {
	var (
		ctx     context.Context
		clock   *fakeClock
		backend *sqlite.Backend
	)

	counted := func(counts ...int) []recordstore.Row {
		rows := make([]recordstore.Row, len(counts))
		for index, count := range counts {
			rows[index] = recordstore.Row{"name": recordstoretest.SampleRow(index + 1)["name"], "count": count}
		}
		return rows
	}
	seqs := func(stream string) []int64 {
		seqs, _ := recordstoretest.Scanned(backend, stream, 0)
		return seqs
	}
	meta := func(stream string) recordstore.Meta {
		meta, err := backend.Meta(ctx, stream)
		Expect(err).ToNot(HaveOccurred())
		return meta
	}

	BeforeEach(func() {
		ctx = context.Background()
		clock = &fakeClock{now: time.Now()}
		backend = openSQLite(filepath.Join(GinkgoT().TempDir(), "records.sqlite"), clock, recordstoretest.Schema, false)
		DeferCleanup(backend.Close)
	})

	It("drops the rows its rules select, keeping the last row, and counts the compaction", func() {
		_, err := backend.Append(ctx, "run-1", recordstoretest.CompactingKind, counted(5, 1, 7, 2, 9))
		Expect(err).ToNot(HaveOccurred())

		removed, err := backend.Compact(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).To(Equal(2))
		Expect(seqs("run-1")).To(Equal([]int64{2, 4, 5}), "seq 5 matches but is the high seq, which always stays")
		Expect(meta("run-1")).To(And(
			HaveField("Total", int64(3)), HaveField("LowSeq", int64(2)), HaveField("HighSeq", int64(5)),
			HaveField("Compactions", int64(1)),
		))

		removed, err = backend.Compact(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).To(BeZero())
		Expect(meta("run-1").Compactions).To(Equal(int64(1)), "a compaction that drops nothing is not counted")
	})

	It("frees the key of a row it drops", func() {
		_, err := backend.Append(ctx, "run-1", recordstoretest.CompactingKind, counted(5, 1))
		Expect(err).ToNot(HaveOccurred())
		_, err = backend.Compact(ctx)
		Expect(err).ToNot(HaveOccurred())

		result, err := backend.Append(ctx, "run-1", recordstoretest.CompactingKind, counted(1))
		Expect(err).ToNot(HaveOccurred())
		Expect(result).To(Equal(recordstore.AppendResult{Window: recordstore.Window{From: 3, To: 3}}))
	})

	It("drops only rows older than a rule's age, by the kind's time column", func() {
		timed := func(kind string) (recordstore.KindSchema, error) {
			return recordstore.KindSchema{Kind: kind, Columns: []query.ColumnDef{
				{Name: "name", Type: query.ColumnTypeString}, {Name: "at", Type: query.ColumnTypeDateTime},
			}, Options: recordstore.KindOptions{TimeColumn: "at", Compact: []recordstore.CompactRule{{OlderThan: time.Hour}}}}, nil
		}
		aged := openSQLite(filepath.Join(GinkgoT().TempDir(), "aged.sqlite"), clock, timed, false)
		DeferCleanup(aged.Close)
		now := clock.Now()
		_, err := aged.Append(ctx, "run-1", "timed", []recordstore.Row{
			{"name": "old", "at": now.Add(-3 * time.Hour)}, {"name": "new", "at": now.Add(-time.Minute)},
			{"name": "older", "at": now.Add(-2 * time.Hour)}, {"name": "last", "at": now.Add(-4 * time.Hour)},
		})
		Expect(err).ToNot(HaveOccurred())

		removed, err := aged.Compact(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).To(Equal(2))
		kept, _ := recordstoretest.Scanned(aged, "run-1", 0)
		Expect(kept).To(Equal([]int64{2, 4}))
	})

	It("reports a rule that does not evaluate to a boolean, dropping nothing", func() {
		broken := func(kind string) (recordstore.KindSchema, error) {
			return recordstore.KindSchema{Kind: kind, Columns: recordstoretest.Columns,
				Options: recordstore.KindOptions{Compact: []recordstore.CompactRule{{Where: "row.count + 1"}}}}, nil
		}
		wrong := openSQLite(filepath.Join(GinkgoT().TempDir(), "wrong.sqlite"), clock, broken, false)
		DeferCleanup(wrong.Close)
		_, err := wrong.Append(ctx, "run-1", "broken", counted(5, 5))
		Expect(err).ToNot(HaveOccurred())

		_, err = wrong.Compact(ctx)
		Expect(err).To(MatchError(ContainSubstring("expected a boolean")))
		kept, _ := recordstoretest.Scanned(wrong, "run-1", 0)
		Expect(kept).To(HaveLen(2))
	})

	It("leaves streams of kinds with no rules alone", func() {
		_, err := backend.Append(ctx, "run-1", recordstoretest.KeyedKind, counted(5, 7, 9))
		Expect(err).ToNot(HaveOccurred())
		removed, err := backend.Compact(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).To(BeZero())
	})

	Describe("merging streams", func() {
		named := func(stream string) []string {
			_, rows := recordstoretest.Scanned(backend, stream, 0)
			var names []string
			for _, row := range rows {
				names = append(names, row["name"].(string))
			}
			return names
		}
		appendNamed := func(stream, kind string, names ...string) {
			rows := make([]recordstore.Row, len(names))
			for index, name := range names {
				rows[index] = recordstore.Row{"name": name, "count": index}
			}
			_, err := backend.Append(ctx, stream, kind, rows)
			Expect(err).ToNot(HaveOccurred())
		}

		It("copies the sources' rows into a new stream, in source then seq order", func() {
			appendNamed("a", recordstoretest.Kind, "a1", "a2")
			appendNamed("b", recordstoretest.Kind, "b1")

			result, err := backend.Merge(ctx, "merged", []string{"a", "b"}, recordstore.MergeOptions{})
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Window).To(Equal(recordstore.Window{From: 1, To: 3}))
			Expect(named("merged")).To(Equal([]string{"a1", "a2", "b1"}))
			Expect(named("a")).To(Equal([]string{"a1", "a2"}), "the sources stay")
		})

		It("orders rows by the kind's time column before the sources' order", func() {
			timed := func(kind string) (recordstore.KindSchema, error) {
				return recordstore.KindSchema{Kind: kind, Columns: []query.ColumnDef{
					{Name: "name", Type: query.ColumnTypeString}, {Name: "at", Type: query.ColumnTypeDateTime},
				}, Options: recordstore.KindOptions{TimeColumn: "at"}}, nil
			}
			merging := openSQLite(filepath.Join(GinkgoT().TempDir(), "timed.sqlite"), clock, timed, false)
			DeferCleanup(merging.Close)
			at := clock.Now()
			_, err := merging.Append(ctx, "a", "timed", []recordstore.Row{{"name": "a-late", "at": at.Add(time.Minute)}, {"name": "a-early", "at": at}})
			Expect(err).ToNot(HaveOccurred())
			_, err = merging.Append(ctx, "b", "timed", []recordstore.Row{{"name": "b-mid", "at": at.Add(30 * time.Second)}, {"name": "b-early", "at": at}})
			Expect(err).ToNot(HaveOccurred())

			_, err = merging.Merge(ctx, "merged", []string{"a", "b"}, recordstore.MergeOptions{})
			Expect(err).ToNot(HaveOccurred())
			_, rows := recordstoretest.Scanned(merging, "merged", 0)
			var names []string
			for _, row := range rows {
				names = append(names, row["name"].(string))
			}
			Expect(names).To(Equal([]string{"a-early", "b-early", "b-mid", "a-late"}))
		})

		DescribeTable("resolves a key held by several sources by the kind's conflict policy",
			func(kind string, expected []string) {
				appendNamed("a", kind, "shared", "a-only")
				appendNamed("b", kind, "b-only", "shared")

				_, err := backend.Merge(ctx, "merged", []string{"a", "b"}, recordstore.MergeOptions{})
				Expect(err).ToNot(HaveOccurred())
				_, rows := recordstoretest.Scanned(backend, "merged", 0)
				var described []string
				for _, row := range rows {
					described = append(described, fmt.Sprintf("%s@%v", row["name"], row["count"]))
				}
				Expect(described).To(Equal(expected))
			},
			Entry("skipping keeps the first", recordstoretest.KeyedKind, []string{"shared@0", "a-only@1", "b-only@0"}),
			Entry("replacing keeps the last", recordstoretest.ReplacingKind, []string{"a-only@1", "b-only@0", "shared@1"}),
		)

		It("keeps only the rows a filter selects, and can delete the sources in the same write", func() {
			appendNamed("a", recordstoretest.Kind, "a1", "a2")
			appendNamed("b", recordstoretest.Kind, "b1")

			_, err := backend.Merge(ctx, "merged", []string{"a", "b"}, recordstore.MergeOptions{Where: `row.name != "a2"`, DeleteSources: true})
			Expect(err).ToNot(HaveOccurred())
			Expect(named("merged")).To(Equal([]string{"a1", "b1"}))
			for _, stream := range []string{"a", "b"} {
				_, err := backend.Meta(ctx, stream)
				Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue(), stream)
			}
		})

		It("refuses a target that exists, sources of two kinds, or a source that does not exist", func() {
			appendNamed("a", recordstoretest.Kind, "a1")
			appendNamed("k", recordstoretest.KeyedKind, "k1")

			_, err := backend.Merge(ctx, "a", []string{"a"}, recordstore.MergeOptions{})
			Expect(err).To(MatchError(ContainSubstring("already exists")))
			_, err = backend.Merge(ctx, "merged", []string{"a", "k"}, recordstore.MergeOptions{})
			Expect(err).To(MatchError(ContainSubstring("kind")))
			_, err = backend.Merge(ctx, "merged", []string{"a", "missing"}, recordstore.MergeOptions{})
			Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue(), "Merge: %v", err)
			_, err = backend.Meta(ctx, "merged")
			Expect(errors.Is(err, recordstore.ErrNotFound)).To(BeTrue())
		})
	})
})
