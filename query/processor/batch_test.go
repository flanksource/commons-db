package processor_test

import (
	"slices"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/processor"
	"github.com/flanksource/commons-db/types"
)

// flushedAt is the instant a log shipper flushed a batch of lines. Every
// timestamp in these specs is an offset from it, so the expectations read as
// "same flush" or "12ms later" rather than as opaque literals.
var flushedAt = time.Date(2026, 4, 19, 11, 23, 40, 207_000_000, time.UTC)

func logRow(offset time.Duration, message string, labels ...string) query.Row {
	row := query.Row{"timestamp": flushedAt.Add(offset), "message": message}
	for index := 0; index+1 < len(labels); index += 2 {
		row[labels[index]] = labels[index+1]
	}
	return row
}

func messages(rows []query.Row) []string {
	out := make([]string, len(rows))
	for index, row := range rows {
		out[index], _ = row["message"].(string)
	}
	return out
}

// javaTrace is one settlement failure as a line-oriented shipper stores it:
// six documents, one exception. Ordered oldest first, the way the JVM printed
// it.
var javaTrace = []string{
	"com.acme.billing.SettlementException: gateway rejected batch 88213",
	"\tat com.acme.billing.Settlement.post(Settlement.java:214)",
	"\tat com.acme.billing.InvoiceJob.run(InvoiceJob.java:64)",
	"Caused by: java.net.SocketTimeoutException: Read timed out",
	"\tat java.base/java.net.SocketInputStream.read(SocketInputStream.java:183)",
	"\t... 23 more",
}

// javaLibraryConfig is the shipped "java.stacktrace" preset, decoded the way the
// processor decodes it, so the specs exercise the library entry itself rather
// than a copy of its expressions.
func javaLibraryConfig() processor.BatchConfig {
	resolved, err := query.ProcessorSpec{Use: "java.stacktrace"}.Resolve()
	Expect(err).ToNot(HaveOccurred())
	config, err := query.DecodeOptions[processor.BatchConfig](resolved.Config)
	Expect(err).ToNot(HaveOccurred())
	return config
}

var _ = Describe("cel.batch grouping", func() {
	ctx := context.New()

	It("groups adjacent rows that share a timestamp and leaves later ones alone", func() {
		rows := []query.Row{
			logRow(0, "starting settlement"),
			logRow(0, "loaded 412 invoices"),
			logRow(12*time.Millisecond, "posting to gateway"),
		}

		out, err := processor.ApplyBatch(ctx, rows, processor.BatchConfig{
			Set: map[string]string{"message": `dyn(batch).map(line, line.message + "").join(" | ")`},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(messages(out)).To(Equal([]string{
			"starting settlement | loaded 412 invoices",
			"posting to gateway",
		}))
	})

	It("buckets timestamps by the window before comparing them", func() {
		rows := []query.Row{
			logRow(0, "first"),
			logRow(400*time.Millisecond, "second"),
			logRow(1500*time.Millisecond, "third"),
		}

		out, err := processor.ApplyBatch(ctx, rows, processor.BatchConfig{
			Window: types.Duration{Duration: time.Second},
			Set:    map[string]string{"message": `dyn(batch).map(line, line.message + "").join("+")`},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(messages(out)).To(Equal([]string{"first+second", "third"}))
	})

	It("never merges across a partition even when the timestamps match", func() {
		rows := []query.Row{
			logRow(0, "api line", "pod", "billing-api-7f9"),
			logRow(0, "worker line", "pod", "billing-worker-2c1"),
		}

		out, err := processor.ApplyBatch(ctx, rows, processor.BatchConfig{
			Partition: []string{"pod"},
			Set:       map[string]string{"message": `dyn(batch).map(line, line.message + "").join("+")`},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(messages(out)).To(Equal([]string{"api line", "worker line"}))
	})

	It("ignores a partition column no row carries", func() {
		rows := []query.Row{logRow(0, "one"), logRow(0, "two")}

		out, err := processor.ApplyBatch(ctx, rows, processor.BatchConfig{
			Partition: []string{"container"},
			Set:       map[string]string{"message": `dyn(batch).map(line, line.message + "").join("+")`},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(messages(out)).To(Equal([]string{"one+two"}))
	})

	It("replaces the timestamp rule with an explicit boundary expression", func() {
		rows := []query.Row{
			logRow(0, "GET /invoices", "request", "r-1"),
			logRow(5*time.Millisecond, "authorized", "request", "r-1"),
			logRow(9*time.Millisecond, "GET /batches", "request", "r-2"),
		}

		out, err := processor.ApplyBatch(ctx, rows, processor.BatchConfig{
			Boundary: `row.request != prev.request`,
			Set:      map[string]string{"message": `dyn(batch).map(line, line.message + "").join("+")`},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(messages(out)).To(Equal([]string{"GET /invoices+authorized", "GET /batches"}))
	})

	It("closes a batch at the max, so a runaway continuation cannot fold a whole page", func() {
		rows := make([]query.Row, 5)
		for index := range rows {
			rows[index] = logRow(0, string(rune('a'+index)))
		}

		out, err := processor.ApplyBatch(ctx, rows, processor.BatchConfig{
			Max: 2,
			Set: map[string]string{"message": `dyn(batch).map(line, line.message + "").join("")`},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(messages(out)).To(Equal([]string{"ab", "cd", "e"}))
	})
})

var _ = Describe("cel.batch transform", func() {
	ctx := context.New()

	It("passes a batch the when gate rejects through untouched", func() {
		rows := []query.Row{logRow(0, "alone"), logRow(time.Second, "also alone")}

		out, err := processor.ApplyBatch(ctx, rows, processor.BatchConfig{
			When: "count > 1",
			Set:  map[string]string{"message": `"merged"`},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(Equal(rows))
	})

	It("builds the merged row from the last row when keep is last", func() {
		rows := []query.Row{
			logRow(0, "attempt 1", "attempt", "1"),
			logRow(0, "attempt 2", "attempt", "2"),
		}

		out, err := processor.ApplyBatch(ctx, rows, processor.BatchConfig{
			Keep: processor.KeepLast,
			Set:  map[string]string{"tries": "count"},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(HaveLen(1))
		Expect(out[0]["attempt"]).To(Equal("2"))
		Expect(out[0]["tries"]).To(BeEquivalentTo(2))
	})

	It("evaluates every set expression against the unmodified kept row", func() {
		rows := []query.Row{logRow(0, "boom"), logRow(0, "  at Frame.one")}

		out, err := processor.ApplyBatch(ctx, rows, processor.BatchConfig{
			Set: map[string]string{
				"message":  `"replaced"`,
				"original": `row.message`,
			},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(out[0]["message"]).To(Equal("replaced"))
		Expect(out[0]["original"]).To(Equal("boom"))
	})

	It("fans a batch back out to several rows with emit", func() {
		rows := []query.Row{logRow(0, "a"), logRow(0, "b")}

		out, err := processor.ApplyBatch(ctx, rows, processor.BatchConfig{
			Emit: `dyn(batch).map(line, {"message": line.message, "batch_size": count})`,
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(messages(out)).To(Equal([]string{"a", "b"}))
		Expect(out[0]["batch_size"]).To(BeEquivalentTo(2))
	})
})

var _ = Describe("cel.batch ordering", func() {
	ctx := context.New()

	It("merges a newest-first result chronologically and keeps it newest first", func() {
		chronological := []query.Row{
			logRow(0, "first"),
			logRow(0, "second"),
			logRow(30*time.Millisecond, "later"),
		}
		newestFirst := slices.Clone(chronological)
		slices.Reverse(newestFirst)

		out, err := processor.ApplyBatch(ctx, newestFirst, processor.BatchConfig{
			Order: processor.OrderDescending,
			Set:   map[string]string{"message": `dyn(batch).map(line, line.message + "").join(" ")`},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(messages(out)).To(Equal([]string{"later", "first second"}))
	})
})

var _ = Describe("cel.batch validation", func() {
	ctx := context.New()

	It("rejects a config that sets both set and emit", func() {
		_, err := processor.ApplyBatch(ctx, []query.Row{logRow(0, "x")}, processor.BatchConfig{
			Set:  map[string]string{"message": `""`},
			Emit: `batch`,
		})
		Expect(err).To(MatchError(ContainSubstring("pick one")))
	})

	It("rejects a config with no transform at all", func() {
		_, err := processor.ApplyBatch(ctx, []query.Row{logRow(0, "x")}, processor.BatchConfig{})
		Expect(err).To(MatchError(ContainSubstring("requires either set or emit")))
	})

	It("rejects boundary and continuation together", func() {
		_, err := processor.ApplyBatch(ctx, []query.Row{logRow(0, "x")}, processor.BatchConfig{
			Boundary:     "true",
			Continuation: "true",
			Set:          map[string]string{"message": `""`},
		})
		Expect(err).To(MatchError(ContainSubstring("boundary already replaces the timestamp rule")))
	})

	It("names the available fields when no timestamp column can be found", func() {
		_, err := processor.ApplyBatch(ctx, []query.Row{{"msg": "no time here"}}, processor.BatchConfig{
			Set: map[string]string{"message": `""`},
		})
		Expect(err).To(MatchError(ContainSubstring("no timestamp column found")))
		Expect(err).To(MatchError(ContainSubstring("msg")))
	})

	It("rejects an explicit timestamp column that no row carries", func() {
		_, err := processor.ApplyBatch(ctx, []query.Row{logRow(0, "x")}, processor.BatchConfig{
			Column: "event_time",
			Set:    map[string]string{"message": `""`},
		})
		Expect(err).To(MatchError(ContainSubstring(`timestamp column "event_time" is on no row`)))
	})

	It("fails loudly on a row whose timestamp cannot be parsed", func() {
		rows := []query.Row{logRow(0, "ok"), {"timestamp": "not-a-time", "message": "bad"}}

		_, err := processor.ApplyBatch(ctx, rows, processor.BatchConfig{
			Set: map[string]string{"message": `""`},
		})
		Expect(err).To(MatchError(ContainSubstring("not a recognized time")))
	})

	It("rejects a non-boolean when gate instead of guessing truthiness", func() {
		_, err := processor.ApplyBatch(ctx, []query.Row{logRow(0, "x")}, processor.BatchConfig{
			When: "count",
			Set:  map[string]string{"message": `""`},
		})
		Expect(err).To(MatchError(ContainSubstring("expected a boolean")))
	})

	It("accepts epoch-millisecond timestamps", func() {
		rows := []query.Row{
			{"timestamp": flushedAt.UnixMilli(), "message": "one"},
			{"timestamp": flushedAt.UnixMilli(), "message": "two"},
			{"timestamp": flushedAt.Add(time.Hour).UnixMilli(), "message": "three"},
		}

		out, err := processor.ApplyBatch(ctx, rows, processor.BatchConfig{
			Set: map[string]string{"message": `dyn(batch).map(line, line.message + "").join("+")`},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(messages(out)).To(Equal([]string{"one+two", "three"}))
	})
})

var _ = Describe("java.stacktrace library processor", func() {
	ctx := context.New()

	// The rows as a log search returns them: newest first, each stack line its
	// own document, stamped a millisecond apart by the shipper.
	javaRows := func() []query.Row {
		rows := []query.Row{logRow(-time.Second, "settling batch 88213", "pod", "billing-worker-2c1")}
		for index, line := range javaTrace {
			rows = append(rows, logRow(time.Duration(index)*time.Millisecond, line, "pod", "billing-worker-2c1"))
		}
		rows = append(rows, logRow(2*time.Second, "retrying batch 88213", "pod", "billing-worker-2c1"))
		slices.Reverse(rows)
		return rows
	}

	It("collapses one exception into one row and leaves the lines around it alone", func() {
		out, err := processor.ApplyBatch(ctx, javaRows(), javaLibraryConfig())

		Expect(err).ToNot(HaveOccurred())
		Expect(messages(out)).To(Equal([]string{
			"retrying batch 88213",
			strings.Join(javaTrace, "\n"),
			"settling batch 88213",
		}))
	})

	It("reports the thrown type and the frame count on the merged row", func() {
		out, err := processor.ApplyBatch(ctx, javaRows(), javaLibraryConfig())

		Expect(err).ToNot(HaveOccurred())
		merged := out[1]
		Expect(merged["exception"]).To(Equal("com.acme.billing.SettlementException"))
		Expect(merged["stack_depth"]).To(BeEquivalentTo(len(javaTrace) - 1))
		Expect(merged["pod"]).To(Equal("billing-worker-2c1"))
	})

	It("leaves a plain log line without an exception unannotated", func() {
		rows := []query.Row{
			logRow(0, "connection pool exhausted"),
			logRow(0, "queue depth 4210"),
		}

		out, err := processor.ApplyBatch(ctx, rows, javaLibraryConfig())

		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(HaveLen(1))
		Expect(out[0]["exception"]).To(Equal(""))
	})

	It("survives a row the provider returned without a message", func() {
		rows := []query.Row{
			{"timestamp": flushedAt.Add(-time.Second), "pod": "billing-worker-2c1"},
			logRow(0, javaTrace[0], "pod", "billing-worker-2c1"),
			logRow(time.Millisecond, javaTrace[1], "pod", "billing-worker-2c1"),
		}
		slices.Reverse(rows)

		out, err := processor.ApplyBatch(ctx, rows, javaLibraryConfig())

		Expect(err).ToNot(HaveOccurred())
		Expect(messages(out)).To(ContainElement(javaTrace[0] + "\n" + javaTrace[1]))
	})

	It("keeps two exceptions from different pods separate", func() {
		rows := []query.Row{
			logRow(0, javaTrace[0], "pod", "billing-worker-2c1"),
			logRow(0, javaTrace[0], "pod", "billing-api-7f9"),
			logRow(time.Millisecond, javaTrace[1], "pod", "billing-api-7f9"),
		}
		slices.Reverse(rows)

		out, err := processor.ApplyBatch(ctx, rows, javaLibraryConfig())

		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(HaveLen(2))
		Expect(messages(out)).To(ContainElement(javaTrace[0] + "\n" + javaTrace[1]))
		Expect(messages(out)).To(ContainElement(javaTrace[0]))
	})
})

var _ = Describe("processor library", func() {
	It("resolves a library reference into a self-contained spec", func() {
		resolved, err := query.ProcessorSpec{Use: "java.stacktrace"}.Resolve()

		Expect(err).ToNot(HaveOccurred())
		Expect(resolved.Type).To(Equal("cel.batch"))
		Expect(resolved.Config).To(HaveKeyWithValue("order", processor.OrderDescending))
		Expect(resolved.Config).To(HaveKeyWithValue("max", 500))
	})

	It("merges an override into the preset without dropping its siblings", func() {
		resolved, err := query.ProcessorSpec{
			Use:    "java.stacktrace",
			Config: map[string]any{"order": processor.OrderAscending, "set": map[string]any{"stack_depth": "count"}},
		}.Resolve()

		Expect(err).ToNot(HaveOccurred())
		Expect(resolved.Config["order"]).To(Equal(processor.OrderAscending))
		set := resolved.Config["set"].(map[string]any)
		Expect(set["stack_depth"]).To(Equal("count"))
		Expect(set).To(HaveKey("message"))
	})

	It("lists the available names when the reference is unknown", func() {
		_, err := query.ProcessorSpec{Use: "java.stacktracee"}.Resolve()

		Expect(err).To(MatchError(ContainSubstring("java.stacktrace")))
	})

	It("rejects a spec that names neither a type nor a library entry", func() {
		_, err := query.ProcessorSpec{}.Resolve()

		Expect(err).To(MatchError(ContainSubstring("requires either type or use")))
	})

	It("rejects a type that contradicts the library entry", func() {
		_, err := query.ProcessorSpec{Use: "java.stacktrace", Type: "sqlite.merge"}.Resolve()

		Expect(err).To(MatchError(ContainSubstring("is type \"cel.batch\"")))
	})
})
