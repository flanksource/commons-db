package query_test

import (
	"time"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// columnNames extracts the schema keys in order.
func columnNames(columns []api.ColumnDef) []string {
	names := make([]string, len(columns))
	for i, column := range columns {
		names[i] = column.Name
	}
	return names
}

// firstFlattened reconciles the given rows and returns the single flattened row.
func firstFlattened(source, dest []query.Row, sourceProfile, destProfile query.Profile) query.Row {
	GinkgoHelper()
	result, err := query.Reconcile(reconcileCtx(),
		&query.Result{Rows: source}, &query.Result{Rows: dest},
		sourceProfile, destProfile, query.ReconcileSpec{Key: query.KeySpec{CEL: `row.id`}})
	Expect(err).ToNot(HaveOccurred())
	Expect(result.Rows).To(HaveLen(1))
	return result.Flatten()[0]
}

var _ = Describe("ReconcileResult rendering", func() {
	sourceProfile := query.Profile{
		Name: "outgoing",
		Columns: []query.ColumnDef{
			{Name: "sent_at", Kind: query.ColumnKindTimestamp, Type: query.ColumnTypeDateTime},
			{Name: "id", Label: "Message"},
			{Name: "internal", Hidden: true},
		},
	}
	destProfile := query.Profile{
		Name:    "incoming",
		Columns: []query.ColumnDef{{Name: "received_at", Kind: query.ColumnKindTimestamp}, {Name: "id"}},
	}

	It("emits the join columns before each side's columns", func() {
		result, err := query.Reconcile(reconcileCtx(),
			&query.Result{Rows: []query.Row{{"sent_at": reconcileEpoch, "id": "A"}}},
			&query.Result{Rows: []query.Row{{"received_at": reconcileEpoch, "id": "A"}}},
			sourceProfile, destProfile, query.ReconcileSpec{Key: query.KeySpec{CEL: `row.id`}})
		Expect(err).ToNot(HaveOccurred())

		Expect(columnNames(result.Columns())).To(Equal([]string{
			"key", "status", "source_time", "dest_time", "time_diff",
			"outgoing_sent_at", "outgoing_id",
			"incoming_received_at", "incoming_id",
		}))
	})

	It("qualifies both sides by side when a profile is reconciled against itself", func() {
		same := query.Profile{Name: "orders", Columns: []query.ColumnDef{{Name: "id"}}}
		result, err := query.Reconcile(reconcileCtx(),
			&query.Result{Rows: []query.Row{{"id": "A"}}}, &query.Result{Rows: []query.Row{{"id": "A"}}},
			same, same, query.ReconcileSpec{Key: query.KeySpec{CEL: `row.id`}})
		Expect(err).ToNot(HaveOccurred())

		Expect(columnNames(result.Columns())).To(ContainElements("orders_src_id", "orders_dest_id"))
	})

	It("omits hidden columns from the rendered schema", func() {
		result, err := query.Reconcile(reconcileCtx(),
			&query.Result{Rows: []query.Row{{"id": "A", "internal": "secret"}}},
			&query.Result{Rows: []query.Row{{"id": "A"}}},
			sourceProfile, destProfile, query.ReconcileSpec{Key: query.KeySpec{CEL: `row.id`}})
		Expect(err).ToNot(HaveOccurred())
		Expect(columnNames(result.Columns())).ToNot(ContainElement("outgoing_internal"))
	})

	It("blanks the missing side rather than dropping its keys", func() {
		flattened := firstFlattened(
			[]query.Row{{"sent_at": reconcileEpoch, "id": "A"}}, nil, sourceProfile, destProfile)

		Expect(flattened).To(HaveKeyWithValue("outgoing_id", "A"))
		Expect(flattened).To(HaveKeyWithValue("incoming_id", ""))
		Expect(flattened).To(HaveKeyWithValue("dest_time", ""))
	})

	It("labels each row of a duplicated key with its position", func() {
		result, err := query.Reconcile(reconcileCtx(),
			&query.Result{Rows: []query.Row{{"id": "A"}, {"id": "A"}}},
			&query.Result{Rows: []query.Row{{"id": "A"}}},
			sourceProfile, destProfile, query.ReconcileSpec{Key: query.KeySpec{CEL: `row.id`}})
		Expect(err).ToNot(HaveOccurred())

		Expect(result.Flatten()[0]["key"]).To(Equal(api.Text{Content: "A (src 1/2)", Style: "text-amber-500"}))
	})

	It("marks an empty key rather than rendering a blank cell", func() {
		flattened := firstFlattened([]query.Row{{"id": nil}}, nil, sourceProfile, destProfile)
		Expect(flattened["key"]).To(Equal(api.Text{Content: "(empty)", Style: "text-gray-500"}))
	})

	DescribeTable("renders the destination lag",
		func(lag time.Duration, expected any) {
			flattened := firstFlattened(
				[]query.Row{{"sent_at": reconcileEpoch, "id": "A"}},
				[]query.Row{{"received_at": reconcileEpoch.Add(lag), "id": "A"}},
				sourceProfile, destProfile)
			Expect(flattened["time_diff"]).To(Equal(expected))
		},
		Entry("sub-second lag stays plain", 250*time.Millisecond, any("250ms")),
		Entry("a few seconds stays plain", 2*time.Second, any("2.00s")),
		Entry("past the alarm threshold turns red", 30*time.Second, any(api.Text{Content: "30.00s", Style: "text-red-500"})),
		Entry("arriving before it was sent also alarms", -30*time.Second, any(api.Text{Content: "-30.00s", Style: "text-red-500"})),
	)

	It("renders a header-only table when nothing reconciled", func() {
		result, err := query.Reconcile(reconcileCtx(), &query.Result{}, &query.Result{},
			sourceProfile, destProfile, query.ReconcileSpec{Key: query.KeySpec{CEL: `row.id`}})
		Expect(err).ToNot(HaveOccurred())

		table := result.Table()
		Expect(table.Headers).ToNot(BeEmpty())
		Expect(table.Rows).To(BeEmpty())
	})

	It("leads the pretty output with the run summary", func() {
		result, err := query.Reconcile(reconcileCtx(),
			&query.Result{Rows: []query.Row{{"id": "A"}, {"id": "B"}}},
			&query.Result{Rows: []query.Row{{"id": "A"}}},
			sourceProfile, destProfile, query.ReconcileSpec{Key: query.KeySpec{CEL: `row.id`}})
		Expect(err).ToNot(HaveOccurred())

		Expect(result.Pretty().Content).To(Equal(
			"outgoing -> incoming  matched=1 only-source=1 only-dest=0 dup-keys=0\n"))
	})

	It("renders through the clicky formatters", func() {
		result, err := query.Reconcile(reconcileCtx(),
			&query.Result{Rows: []query.Row{{"sent_at": reconcileEpoch, "id": "A"}}},
			&query.Result{Rows: []query.Row{{"received_at": reconcileEpoch, "id": "A"}}},
			sourceProfile, destProfile, query.ReconcileSpec{Key: query.KeySpec{CEL: `row.id`}})
		Expect(err).ToNot(HaveOccurred())

		csv, err := result.Render("csv")
		Expect(err).ToNot(HaveOccurred())
		Expect(csv).To(ContainSubstring("A"))
	})
})
