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
)

var _ = Describe("cel.batch paging", func() {
	ctx := context.New()
	config := map[string]any{
		"set": map[string]any{
			"message": `dyn(batch).map(line, line.message + "").join("+")`,
		},
	}

	pageProcessor := func() query.PageProcessor {
		registered, err := query.GetProcessor("cel.batch")
		Expect(err).ToNot(HaveOccurred())
		paged, ok := registered.(query.PageProcessor)
		Expect(ok).To(BeTrue())
		return paged
	}

	It("makes batch profiles streamable", func() {
		streamable, err := query.StreamableProcessors([]query.ProcessorSpec{{Type: "cel.batch", Config: config}})
		Expect(err).ToNot(HaveOccurred())
		Expect(streamable).To(BeTrue())
	})

	It("carries an ascending batch split across provider pages", func() {
		first, state, err := pageProcessor().ProcessPage(ctx, query.ProcessorSpec{Config: config}, query.Page{
			Rows: []query.Row{
				logRow(0, "one"),
				logRow(0, "two"),
				logRow(time.Second, "three"),
			},
			HasMore: true,
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(messages(first.Rows)).To(Equal([]string{"one+two"}))
		Expect(state).ToNot(BeEmpty())

		second, next, err := pageProcessor().ProcessPage(ctx, query.ProcessorSpec{Config: config}, query.Page{
			Rows: []query.Row{
				logRow(time.Second, "four"),
				logRow(2*time.Second, "five"),
			},
		}, state)
		Expect(err).ToNot(HaveOccurred())
		Expect(messages(second.Rows)).To(Equal([]string{"three+four", "five"}))
		Expect(next).To(BeEmpty())
	})

	It("carries the oldest edge of a descending page", func() {
		descending := map[string]any{
			"order": processor.OrderDescending,
			"set":   config["set"],
		}
		first, state, err := pageProcessor().ProcessPage(ctx, query.ProcessorSpec{Config: descending}, query.Page{
			Rows: []query.Row{
				logRow(2*time.Second, "later"),
				logRow(time.Second, "three"),
				logRow(time.Second, "two"),
			},
			HasMore: true,
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(messages(first.Rows)).To(Equal([]string{"later"}))

		second, next, err := pageProcessor().ProcessPage(ctx, query.ProcessorSpec{Config: descending}, query.Page{
			Rows: []query.Row{
				logRow(time.Second, "one"),
				logRow(0, "older"),
			},
		}, state)
		Expect(err).ToNot(HaveOccurred())
		Expect(messages(second.Rows)).To(Equal([]string{"one+two+three", "older"}))
		Expect(next).To(BeEmpty())
	})

	It("emits a descending edge batch once it reaches max", func() {
		descending := map[string]any{
			"max":   3,
			"order": processor.OrderDescending,
			"set":   config["set"],
		}
		first, state, err := pageProcessor().ProcessPage(ctx, query.ProcessorSpec{Config: descending}, query.Page{
			Rows: []query.Row{
				logRow(0, "three"),
				logRow(0, "two"),
			},
			HasMore: true,
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(first.Rows).To(BeEmpty())

		second, next, err := pageProcessor().ProcessPage(ctx, query.ProcessorSpec{Config: descending}, query.Page{
			Rows:    []query.Row{logRow(0, "one")},
			HasMore: true,
		}, state)
		Expect(err).ToNot(HaveOccurred())
		Expect(messages(second.Rows)).To(Equal([]string{"one+two+three"}))
		Expect(next).To(BeEmpty())
	})

	It("preserves a Java stack trace split across descending pages", func() {
		spec, err := query.ProcessorSpec{Use: "java.stacktrace"}.Resolve()
		Expect(err).ToNot(HaveOccurred())

		rows := []query.Row{logRow(-time.Second, "settling batch 88213", "pod", "billing-worker-2c1")}
		for index, line := range javaTrace {
			rows = append(rows, logRow(time.Duration(index)*time.Millisecond, line, "pod", "billing-worker-2c1"))
		}
		rows = append(rows, logRow(2*time.Second, "retrying batch 88213", "pod", "billing-worker-2c1"))
		slices.Reverse(rows)

		first, state, err := pageProcessor().ProcessPage(ctx, spec, query.Page{
			Rows:    rows[:4],
			HasMore: true,
		}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(messages(first.Rows)).To(Equal([]string{"retrying batch 88213"}))

		second, next, err := pageProcessor().ProcessPage(ctx, spec, query.Page{Rows: rows[4:]}, state)
		Expect(err).ToNot(HaveOccurred())
		Expect(messages(second.Rows)).To(Equal([]string{
			strings.Join(javaTrace, "\n"),
			"settling batch 88213",
		}))
		Expect(next).To(BeEmpty())
	})

	It("rejects malformed carried state", func() {
		_, _, err := pageProcessor().ProcessPage(ctx, query.ProcessorSpec{Config: config}, query.Page{
			Rows: []query.Row{logRow(0, "one")},
		}, []byte("not batch state"))
		Expect(err).To(MatchError(ContainSubstring("batch state")))
	})
})
