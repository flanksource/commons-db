package query_test

import (
	gocontext "context"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The two sides deliberately name the shared identity differently — nested
// under correlation on the way out, flat on the way in. One expression reads
// both, which is precisely what a column-name key cannot do.
const normalisedKeyCEL = `has(row.trace_id) ? row.trace_id : row.correlation.id`

var reconcileEpoch = time.Date(2026, 4, 9, 10, 0, 0, 0, time.UTC)

func outgoingProfile() query.Profile {
	return query.Profile{
		Name: "jms.outgoing",
		Columns: []query.ColumnDef{
			{Name: "sent_at", Kind: query.ColumnKindTimestamp, Type: query.ColumnTypeDateTime},
			{Name: "correlation"},
		},
	}
}

func incomingProfile() query.Profile {
	return query.Profile{
		Name: "jms.incoming",
		Columns: []query.ColumnDef{
			{Name: "received_at", Kind: query.ColumnKindTimestamp, Type: query.ColumnTypeDateTime},
			{Name: "trace_id"},
		},
	}
}

func outgoingRow(id string, offset time.Duration) query.Row {
	return query.Row{
		"sent_at":     reconcileEpoch.Add(offset),
		"correlation": map[string]any{"id": id},
	}
}

func incomingRow(id string, offset time.Duration) query.Row {
	return query.Row{
		"received_at": reconcileEpoch.Add(offset),
		"trace_id":    id,
	}
}

// reconcileCtx is a bare context; Reconcile touches no database.
func reconcileCtx() dbcontext.Context {
	return dbcontext.NewContext(gocontext.Background())
}

// rowsByStatus indexes the emitted rows by key for each status.
func rowsByStatus(result *query.ReconcileResult, status query.ReconcileStatus) []query.ReconcileRow {
	var out []query.ReconcileRow
	for _, row := range result.Rows {
		if row.Status == status {
			out = append(out, row)
		}
	}
	return out
}

var _ = Describe("Reconcile", func() {
	source := &query.Result{Rows: []query.Row{
		outgoingRow("A", 0),
		outgoingRow("B", time.Second),
		outgoingRow("C", 2*time.Second),
	}}
	dest := &query.Result{Rows: []query.Row{
		incomingRow("A", 250*time.Millisecond),
		incomingRow("C", 30*time.Second),
		incomingRow("D", 3*time.Second),
	}}

	reconcile := func(spec query.ReconcileSpec) (*query.ReconcileResult, error) {
		return query.Reconcile(reconcileCtx(), source, dest, outgoingProfile(), incomingProfile(), spec)
	}

	// Both sides need their own key expression, but ReconcileSpec carries one.
	// The engine evaluates the same expression against both, so a cross-schema
	// join goes through an alias that normalises the field name — modelled here
	// by projecting each side onto the same key column first.
	It("joins two differently shaped profiles on a CEL key", func() {
		aliasedSource := &query.Result{Rows: []query.Row{
			{"sent_at": reconcileEpoch, "id": "A"},
			{"sent_at": reconcileEpoch.Add(time.Second), "id": "B"},
		}}
		aliasedDest := &query.Result{Rows: []query.Row{
			{"received_at": reconcileEpoch.Add(250 * time.Millisecond), "id": "A"},
		}}

		result, err := query.Reconcile(reconcileCtx(), aliasedSource, aliasedDest,
			outgoingProfile(), incomingProfile(), query.ReconcileSpec{Key: query.KeySpec{CEL: `row.id`}})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Stats).To(Equal(query.ReconcileStats{Matched: 1, OnlySource: 1}))
	})

	It("classifies keys as matched, only source, and only dest", func() {
		result, err := reconcile(query.ReconcileSpec{Key: query.KeySpec{CEL: normalisedKeyCEL}})
		Expect(err).ToNot(HaveOccurred())

		Expect(result.Stats.Matched).To(Equal(2))
		Expect(result.Stats.OnlySource).To(Equal(1))
		Expect(result.Stats.OnlyDest).To(Equal(1))
		Expect(result.Stats.DupKeys).To(BeZero())

		Expect(rowsByStatus(result, query.ReconcileOnlySource)).To(HaveLen(1))
		Expect(rowsByStatus(result, query.ReconcileOnlySource)[0].Key).To(Equal("B"))
		Expect(rowsByStatus(result, query.ReconcileOnlyDest)[0].Key).To(Equal("D"))
	})

	It("computes the destination lag for matched keys", func() {
		result, err := reconcile(query.ReconcileSpec{Key: query.KeySpec{CEL: normalisedKeyCEL}})
		Expect(err).ToNot(HaveOccurred())

		matched := rowsByStatus(result, query.ReconcileMatched)
		Expect(matched).To(HaveLen(2))
		Expect(matched[0].Key).To(Equal("A"))
		Expect(*matched[0].TimeDiff).To(Equal(250 * time.Millisecond))
		Expect(matched[1].Key).To(Equal("C"))
		Expect(*matched[1].TimeDiff).To(Equal(28 * time.Second))
	})

	It("emits every pair when a key repeats, oldest first", func() {
		duplicated := &query.Result{Rows: []query.Row{
			{"sent_at": reconcileEpoch.Add(time.Second), "id": "A"},
			{"sent_at": reconcileEpoch, "id": "A"},
		}}
		arrivals := &query.Result{Rows: []query.Row{
			{"received_at": reconcileEpoch.Add(2 * time.Second), "id": "A"},
			{"received_at": reconcileEpoch.Add(3 * time.Second), "id": "A"},
			{"received_at": reconcileEpoch.Add(4 * time.Second), "id": "A"},
		}}

		result, err := query.Reconcile(reconcileCtx(), duplicated, arrivals,
			outgoingProfile(), incomingProfile(), query.ReconcileSpec{Key: query.KeySpec{CEL: `row.id`}})
		Expect(err).ToNot(HaveOccurred())

		Expect(result.Rows).To(HaveLen(6))
		Expect(result.Stats).To(Equal(query.ReconcileStats{Matched: 1, DupKeys: 1}))
		Expect(result.Rows[0].SourceDupIndex).To(Equal(1))
		Expect(result.Rows[0].SourceDupCount).To(Equal(2))
		Expect(result.Rows[0].DestDupCount).To(Equal(3))
		// Oldest source first: the row stamped at the epoch, not the one a
		// second later that happened to be listed first.
		Expect(*result.Rows[0].SourceTime).To(Equal(reconcileEpoch))
	})

	It("groups rows whose key resolves to nothing under the empty key", func() {
		missing := &query.Result{Rows: []query.Row{{"sent_at": reconcileEpoch, "id": nil}}}
		result, err := query.Reconcile(reconcileCtx(), missing, &query.Result{},
			outgoingProfile(), incomingProfile(), query.ReconcileSpec{Key: query.KeySpec{CEL: `row.id`}})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows).To(HaveLen(1))
		Expect(result.Rows[0].Key).To(BeEmpty())
		Expect(result.Rows[0].Status).To(Equal(query.ReconcileOnlySource))
	})

	It("supports a column key when both sides share a schema", func() {
		left := &query.Result{Rows: []query.Row{{"region": "eu", "id": "1"}}}
		right := &query.Result{Rows: []query.Row{{"region": "eu", "id": "1"}, {"region": "us", "id": "1"}}}

		result, err := query.Reconcile(reconcileCtx(), left, right,
			query.Profile{Name: "same"}, query.Profile{Name: "same"},
			query.ReconcileSpec{Key: query.KeySpec{Columns: []string{"region", "id"}}})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Stats).To(Equal(query.ReconcileStats{Matched: 1, OnlyDest: 1}))
	})

	It("rejects a key that names neither columns nor an expression", func() {
		_, err := reconcile(query.ReconcileSpec{})
		Expect(err).To(MatchError(ContainSubstring("columns or a cel expression")))
	})

	It("lists the available variables when the key references an unknown field", func() {
		_, err := reconcile(query.ReconcileSpec{Key: query.KeySpec{CEL: `nonexistent_field`}})
		Expect(err).To(MatchError(ContainSubstring("available variables")))
	})

	It("fails loudly when an explicit time column is absent", func() {
		_, err := reconcile(query.ReconcileSpec{
			Key:        query.KeySpec{CEL: normalisedKeyCEL},
			TimeColumn: "definitely_not_a_column",
		})
		Expect(err).To(MatchError(ContainSubstring("is not present on any row")))
	})

	It("omits the time fields when neither profile declares a timestamp column", func() {
		untimed := query.Profile{Name: "untimed"}
		result, err := query.Reconcile(reconcileCtx(),
			&query.Result{Rows: []query.Row{{"id": "A"}}},
			&query.Result{Rows: []query.Row{{"id": "A"}}},
			untimed, untimed, query.ReconcileSpec{Key: query.KeySpec{CEL: `row.id`}})
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Rows[0].SourceTime).To(BeNil())
		Expect(result.Rows[0].TimeDiff).To(BeNil())
	})

	DescribeTable("parses the timestamp shapes providers return",
		func(sent, received any, expected time.Duration) {
			result, err := query.Reconcile(reconcileCtx(),
				&query.Result{Rows: []query.Row{{"sent_at": sent, "id": "A"}}},
				&query.Result{Rows: []query.Row{{"received_at": received, "id": "A"}}},
				outgoingProfile(), incomingProfile(), query.ReconcileSpec{Key: query.KeySpec{CEL: `row.id`}})
			Expect(err).ToNot(HaveOccurred())
			Expect(*result.Rows[0].TimeDiff).To(Equal(expected))
		},
		Entry("RFC3339 strings", reconcileEpoch.Format(time.RFC3339), reconcileEpoch.Add(time.Second).Format(time.RFC3339), time.Second),
		Entry("epoch seconds", reconcileEpoch.Unix(), reconcileEpoch.Add(2*time.Second).Unix(), 2*time.Second),
		Entry("epoch millis", reconcileEpoch.UnixMilli(), reconcileEpoch.Add(1500*time.Millisecond).UnixMilli(), 1500*time.Millisecond),
		Entry("epoch nanos", reconcileEpoch.UnixNano(), reconcileEpoch.Add(time.Minute).UnixNano(), time.Minute),
	)

	It("rejects a timestamp it cannot parse", func() {
		_, err := query.Reconcile(reconcileCtx(),
			&query.Result{Rows: []query.Row{{"sent_at": "not a time", "id": "A"}}},
			&query.Result{}, outgoingProfile(), incomingProfile(),
			query.ReconcileSpec{Key: query.KeySpec{CEL: `row.id`}})
		Expect(err).To(MatchError(ContainSubstring("cannot parse")))
	})
})
