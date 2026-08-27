package query_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
)

// sortingMockProvider applies an order the request names; the bare mockProvider
// stands in for the ones that ignore it.
type sortingMockProvider struct{ mockProvider }

func (*sortingMockProvider) SupportsRequestSort() bool { return true }

var _ = BeforeSuite(func() {
	query.RegisterProvider(&sortingMockProvider{mockProvider{typ: "sorting-mock"}})
	query.RegisterProvider(&mockProvider{typ: "unsorting-mock"})
})

// sortableProfile is a profile whose declared order ends in a unique tiebreaker,
// which is the shape every pageable profile has.
func sortableProfile() query.Profile {
	return query.Profile{
		Name:     "snapshot",
		Provider: query.ProviderConfig{Type: "sorting-mock"},
		Query:    `SELECT "key", "status", "row_id" FROM t`,
		Columns: []query.ColumnDef{
			{Name: "key"},
			{Name: "status", Type: query.ColumnTypeStatus},
			{Name: "time_diff", Type: query.ColumnTypeDuration},
			{Name: "row_id", Type: query.ColumnTypeNumber, Hidden: true},
		},
		Order: query.Order{{Column: "row_id", Unique: true}},
	}
}

var _ = Describe("Profile.SortBindings", func() {
	It("offers every visible column that addresses a backend field", func() {
		bindings, err := sortableProfile().SortBindings()
		Expect(err).ToNot(HaveOccurred())

		columns := make([]string, 0, len(bindings))
		for _, binding := range bindings {
			columns = append(columns, binding.Column)
		}
		Expect(columns).To(Equal([]string{"key", "status", "time_diff"}))
	})

	It("resolves a renamed column to the field the backend actually holds", func() {
		profile := sortableProfile()
		profile.Columns = append(profile.Columns, query.ColumnDef{Name: "age", Source: "created_at"})

		bindings, err := profile.SortBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(ContainElement(query.SortBinding{Column: "age", Field: "created_at"}))
	})

	It("offers nothing for a provider that ignores a requested order", func() {
		profile := sortableProfile()
		profile.Provider = query.ProviderConfig{Type: "unsorting-mock"}

		bindings, err := profile.SortBindings()
		Expect(err).ToNot(HaveOccurred())
		Expect(bindings).To(BeEmpty())
	})

	It("omits a column computed after the row was read", func() {
		profile := sortableProfile()
		profile.Columns = append(profile.Columns, query.ColumnDef{Name: "ratio", CEL: `row.hits / row.total`})

		bindings, err := profile.SortBindings()
		Expect(err).ToNot(HaveOccurred())
		for _, binding := range bindings {
			Expect(binding.Column).ToNot(Equal("ratio"))
		}
	})
})

var _ = Describe("Profile.RequestedOrder", func() {
	It("keeps the declared order when nothing is requested", func() {
		order, err := sortableProfile().RequestedOrder("", false)
		Expect(err).ToNot(HaveOccurred())
		Expect(order).To(Equal(query.Order{{Column: "row_id", Unique: true}}))
	})

	It("leads with the requested column and keeps the tiebreaker last", func() {
		order, err := sortableProfile().RequestedOrder("status", true)
		Expect(err).ToNot(HaveOccurred())
		Expect(order).To(Equal(query.Order{
			{Column: "status", Desc: true},
			{Column: "row_id", Unique: true},
		}))
		Expect(order.Validate()).To(Succeed())
		Expect(order.Pageable()).To(Succeed())
	})

	It("orders by the tiebreaker alone when it is what was asked for", func() {
		profile := sortableProfile()
		profile.Columns[3].Hidden = false

		order, err := profile.RequestedOrder("row_id", true)
		Expect(err).ToNot(HaveOccurred())
		Expect(order).To(Equal(query.Order{{Column: "row_id", Desc: true, Unique: true}}))
		Expect(order.Pageable()).To(Succeed())
	})

	It("does not order by a column twice", func() {
		profile := sortableProfile()
		profile.Order = query.Order{{Column: "status"}, {Column: "row_id", Unique: true}}

		order, err := profile.RequestedOrder("status", true)
		Expect(err).ToNot(HaveOccurred())
		Expect(order).To(Equal(query.Order{
			{Column: "status", Desc: true},
			{Column: "row_id", Unique: true},
		}))
		Expect(order.Validate()).To(Succeed())
	})

	It("names the sortable columns when asked for one that is not", func() {
		_, err := sortableProfile().RequestedOrder("nonesuch", false)
		Expect(err).To(MatchError(ContainSubstring("nonesuch")))
		Expect(err).To(MatchError(ContainSubstring("key, status, time_diff")))
	})

	It("refuses a hidden column, which no caller can see to ask for", func() {
		_, err := sortableProfile().RequestedOrder("row_id", false)
		Expect(err).To(MatchError(ContainSubstring("row_id")))
	})

	It("refuses a sort against a provider that would ignore it", func() {
		profile := sortableProfile()
		profile.Provider = query.ProviderConfig{Type: "unsorting-mock"}

		_, err := profile.RequestedOrder("status", false)
		Expect(err).To(MatchError(ContainSubstring("no sortable columns")))
	})
})
