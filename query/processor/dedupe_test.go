package processor_test

import (
	"fmt"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/processor"
)

// A connection pool exhausting itself logs the same failure repeatedly, but a
// log query returns those lines in time order — interleaved with everything
// else the service was doing. That scattering is the case dedupe exists for and
// the one cel.batch cannot see.
func poolTimeouts() []query.Row {
	return []query.Row{
		logRow(0, "connection pool exhausted", "hash", "pool-exhausted"),
		logRow(30_000_000, "GET /invoices 200", "hash", "get-invoices"),
		logRow(60_000_000, "connection pool exhausted", "hash", "pool-exhausted"),
		logRow(90_000_000, "GET /invoices 200", "hash", "get-invoices"),
		logRow(120_000_000, "connection pool exhausted", "hash", "pool-exhausted"),
	}
}

// dedupeLibraryConfig is the shipped "logs.dedupe" preset, decoded the way the
// processor decodes it, so the specs exercise the library entry rather than a
// copy of its expressions.
func dedupeLibraryConfig() processor.DedupeConfig {
	resolved, err := query.ProcessorSpec{Use: "logs.dedupe"}.Resolve()
	Expect(err).ToNot(HaveOccurred())
	config, err := query.DecodeOptions[processor.DedupeConfig](resolved.Config)
	Expect(err).ToNot(HaveOccurred())
	return config
}

var _ = Describe("cel.dedupe", func() {
	ctx := context.New()

	It("collapses duplicates that are not adjacent", func() {
		rows, err := processor.ApplyDedupe(ctx, poolTimeouts(), processor.DedupeConfig{
			Partition: []string{"hash"},
			Set:       map[string]string{"count": "count"},
		})
		Expect(err).ToNot(HaveOccurred())

		Expect(messages(rows)).To(Equal([]string{
			"connection pool exhausted",
			"GET /invoices 200",
		}))
		Expect(rows[0]["count"]).To(BeNumerically("==", 3))
		Expect(rows[1]["count"]).To(BeNumerically("==", 2))
	})

	It("leaves those same rows alone under cel.batch, which only groups adjacent runs", func() {
		rows, err := processor.ApplyBatch(ctx, poolTimeouts(), processor.BatchConfig{
			Partition: []string{"hash"},
			Boundary:  `row.hash != prev.hash`,
			Set:       map[string]string{"count": "count"},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(5))
	})

	It("keeps groups in the order they first appeared", func() {
		rows, err := processor.ApplyDedupe(ctx, []query.Row{
			logRow(0, "b", "hash", "b"),
			logRow(1_000_000, "a", "hash", "a"),
			logRow(2_000_000, "b", "hash", "b"),
			logRow(3_000_000, "c", "hash", "c"),
		}, processor.DedupeConfig{Partition: []string{"hash"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(messages(rows)).To(Equal([]string{"b", "a", "c"}))
	})

	It("keeps the first row of a group by default and the last on request", func() {
		rows := []query.Row{
			logRow(0, "attempt one", "hash", "retry"),
			logRow(5_000_000, "attempt two", "hash", "retry"),
		}

		first, err := processor.ApplyDedupe(ctx, rows, processor.DedupeConfig{Partition: []string{"hash"}})
		Expect(err).ToNot(HaveOccurred())
		Expect(messages(first)).To(Equal([]string{"attempt one"}))

		last, err := processor.ApplyDedupe(ctx, rows, processor.DedupeConfig{
			Partition: []string{"hash"},
			Keep:      processor.KeepLast,
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(messages(last)).To(Equal([]string{"attempt two"}))
	})

	It("passes a group the when gate rejects through as separate rows", func() {
		rows, err := processor.ApplyDedupe(ctx, poolTimeouts(), processor.DedupeConfig{
			Partition: []string{"hash"},
			When:      "count > 2",
			Set:       map[string]string{"count": "count"},
		})
		Expect(err).ToNot(HaveOccurred())

		// The 3 pool-exhausted lines collapse; the 2 request lines do not, so
		// they arrive still separate and still in place.
		Expect(messages(rows)).To(Equal([]string{
			"connection pool exhausted",
			"GET /invoices 200",
			"GET /invoices 200",
		}))
	})

	It("groups every row together when the partition names a column no row has", func() {
		rows, err := processor.ApplyDedupe(ctx, poolTimeouts(), processor.DedupeConfig{
			Partition: []string{"container"},
			Set:       map[string]string{"count": "count"},
		})
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0]["count"]).To(BeNumerically("==", 5))
	})

	It("starts a fresh group once one reaches max", func() {
		rows, err := processor.ApplyDedupe(ctx, poolTimeouts(), processor.DedupeConfig{
			Partition: []string{"hash"},
			Max:       2,
			Set:       map[string]string{"count": "count"},
		})
		Expect(err).ToNot(HaveOccurred())

		// pool-exhausted appears 3 times: two collapse at the cap, the third
		// opens a new group. get-invoices appears twice and fills one group.
		Expect(messages(rows)).To(Equal([]string{
			"connection pool exhausted",
			"GET /invoices 200",
			"connection pool exhausted",
		}))
		Expect(rows[0]["count"]).To(BeNumerically("==", 2))
		Expect(rows[2]["count"]).To(BeNumerically("==", 1))
	})

	It("refuses a config with no partition, which would collapse the whole result", func() {
		_, err := processor.ApplyDedupe(ctx, poolTimeouts(), processor.DedupeConfig{
			Set: map[string]string{"count": "count"},
		})
		Expect(err).To(MatchError(ContainSubstring("requires partition")))
	})

	Describe("the logs.dedupe preset", func() {
		It("collapses by hash and reports the span it was seen over", func() {
			rows, err := processor.ApplyDedupe(ctx, poolTimeouts(), dedupeLibraryConfig())
			Expect(err).ToNot(HaveOccurred())

			Expect(rows).To(HaveLen(2))
			Expect(rows[0]["count"]).To(BeNumerically("==", 3))
			Expect(rows[0]["firstSeen"]).To(Equal(flushedAt))
			Expect(rows[0]["lastSeen"]).To(Equal(flushedAt.Add(120_000_000)))
		})

		// The two ends of a group are the oldest and newest rows in it whichever
		// way the query sorted. Reading them by position made the labels depend
		// on the provider: a Kubernetes profile pages ascending and a Loki one
		// arrives newest-first, and the preset is shared by both.
		It("labels the same span whichever direction the rows arrived in", func() {
			ascending, err := processor.ApplyDedupe(ctx, poolTimeouts(), dedupeLibraryConfig())
			Expect(err).ToNot(HaveOccurred())

			reversed := poolTimeouts()
			slices.Reverse(reversed)
			descending, err := processor.ApplyDedupe(ctx, reversed, dedupeLibraryConfig())
			Expect(err).ToNot(HaveOccurred())

			Expect(descending[0]["firstSeen"]).To(Equal(ascending[0]["firstSeen"]))
			Expect(descending[0]["lastSeen"]).To(Equal(ascending[0]["lastSeen"]))
		})
	})

	Describe("paging", func() {
		spec := query.ProcessorSpec{
			Type: "cel.dedupe",
			Config: map[string]any{
				"partition": []any{"hash"},
				"set":       map[string]any{"count": "count"},
			},
		}

		pageProcessor := func() query.PageProcessor {
			registered, err := query.GetProcessor("cel.dedupe")
			Expect(err).ToNot(HaveOccurred())
			paged, ok := registered.(query.PageProcessor)
			Expect(ok).To(BeTrue())
			return paged
		}

		page := func(hashes ...string) query.Page {
			rows := make([]query.Row, 0, len(hashes))
			for _, hash := range hashes {
				rows = append(rows, query.Row{"hash": hash, "message": hash})
			}
			return query.Page{Rows: rows, HasMore: true}
		}

		hashesOf := func(p query.Page) []string {
			values := make([]string, 0, len(p.Rows))
			for _, row := range p.Rows {
				values = append(values, fmt.Sprint(row["hash"]))
			}
			return values
		}

		It("reports a profile using it as streamable", func() {
			streamable, err := query.StreamableProcessors([]query.ProcessorSpec{{Use: "logs.dedupe"}})
			Expect(err).ToNot(HaveOccurred())
			Expect(streamable).To(BeTrue())
		})

		It("folds within a page and counts only that page's rows", func() {
			first, _, err := pageProcessor().ProcessPage(ctx, spec, page("a", "a", "b"), nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(hashesOf(first)).To(Equal([]string{"a", "b"}))
			// The whole-result fold would say three; a walk can only count what
			// it has seen by the time it has to send the row.
			Expect(first.Rows[0]["count"]).To(Equal(int64(2)))
		})

		// The groups a walk has emitted grow with every page, and a cursor has a
		// size ceiling, so they are not carried: a long walk keeps going, and a
		// group may surface again on a later page.
		It("carries nothing into the cursor, however many groups a page holds", func() {
			const groupsBeyondCursorCapacity = query.MaxCursorBytes/8 + 500
			hashes := make([]string, 0, groupsBeyondCursorCapacity)
			for i := range groupsBeyondCursorCapacity {
				hashes = append(hashes, fmt.Sprintf("hash-%d", i))
			}
			_, state, err := pageProcessor().ProcessPage(ctx, spec, page(hashes...), nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(state).To(BeNil())

			second, _, err := pageProcessor().ProcessPage(ctx, spec, page("hash-0", "hash-0"), state)
			Expect(err).ToNot(HaveOccurred())
			Expect(hashesOf(second)).To(Equal([]string{"hash-0"}))
		})
	})
})
