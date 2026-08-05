package query_test

import (
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var pageableOrder = query.Order{
	{Column: "created_at"},
	{Column: "id", Unique: true},
}

func scopeWith(params map[string]any) query.CursorScope {
	return query.CursorScope{
		Profile: "orders",
		Order:   pageableOrder,
		Params:  params,
		Roles: map[string]query.ParamRole{
			"tenant": query.ParamRoleFilter,
			"limit":  query.ParamRoleLimit,
			"offset": query.ParamRoleOffset,
			"cursor": query.ParamRoleCursor,
		},
	}
}

var _ = Describe("Order", func() {
	It("accepts an order ending in a unique column", func() {
		Expect(pageableOrder.Validate()).To(Succeed())
		Expect(pageableOrder.Pageable()).To(Succeed())
		Expect(pageableOrder.Columns()).To(Equal([]string{"created_at", "id"}))
	})

	It("rejects a column ordered twice", func() {
		order := query.Order{{Column: "id"}, {Column: "id", Unique: true}}
		Expect(order.Validate()).To(MatchError(ContainSubstring("ordered twice")))
	})

	It("rejects an empty column", func() {
		Expect(query.Order{{Column: " "}}.Validate()).To(MatchError(ContainSubstring("column is required")))
	})

	// Columns after a unique one can never affect the order, so declaring them
	// means the author expected something the order cannot do.
	It("rejects a unique column that is not last", func() {
		order := query.Order{{Column: "id", Unique: true}, {Column: "created_at"}}
		Expect(order.Validate()).To(MatchError(ContainSubstring("is not last")))
	})

	Describe("Pageable", func() {
		It("refuses to page an undeclared order", func() {
			Expect(query.Order{}.Pageable()).To(MatchError(ContainSubstring("no order is declared")))
		})

		It("refuses to page an order with no tiebreaker", func() {
			order := query.Order{{Column: "created_at"}}
			Expect(order.Pageable()).To(MatchError(ContainSubstring("not declared unique")))
		})
	})

	Describe("Fingerprint", func() {
		It("is stable for the same order", func() {
			Expect(pageableOrder.Fingerprint()).To(Equal(pageableOrder.Fingerprint()))
		})

		It("changes with the direction", func() {
			reversed := query.Order{{Column: "created_at", Desc: true}, {Column: "id", Unique: true}}
			Expect(reversed.Fingerprint()).ToNot(Equal(pageableOrder.Fingerprint()))
		})

		It("changes with the columns", func() {
			other := query.Order{{Column: "updated_at"}, {Column: "id", Unique: true}}
			Expect(other.Fingerprint()).ToNot(Equal(pageableOrder.Fingerprint()))
		})
	})
})

var _ = Describe("Cursor", func() {
	scope := scopeWith(map[string]any{"tenant": "acme"})
	keys := []any{"2026-01-01T00:00:00Z", "row-9"}

	It("treats the empty cursor as the start of the result set", func() {
		Expect(query.Cursor("").IsZero()).To(BeTrue())
		position, err := query.DecodeCursor("", scope)
		Expect(err).ToNot(HaveOccurred())
		Expect(position.Keys).To(BeEmpty())
		Expect(position.IsZero()).To(BeTrue())
	})

	It("round-trips the position it was issued for", func() {
		cursor, err := query.EncodeCursor(scope, keys, "pit-1")
		Expect(err).ToNot(HaveOccurred())

		position, err := query.DecodeCursor(cursor, scope)
		Expect(err).ToNot(HaveOccurred())
		Expect(position.Keys).To(Equal(keys))
		Expect(position.PIT).To(Equal("pit-1"))
	})

	// A cursor holds one key per ordered column; anything else would resume from
	// a position the order cannot locate.
	It("refuses to issue a cursor whose keys do not match the order", func() {
		_, err := query.EncodeCursor(scope, []any{"only-one"}, "")
		Expect(err).To(MatchError(ContainSubstring("one key per order column")))
	})

	It("refuses to issue a cursor for an order that cannot be paged", func() {
		unordered := scope
		unordered.Order = query.Order{{Column: "created_at"}}
		_, err := query.EncodeCursor(unordered, []any{"x"}, "")
		Expect(err).To(MatchError(ContainSubstring("not declared unique")))
	})

	// The keys are handed back to the author's template under the column names
	// they declared the order in.
	It("exposes the position to a keyset query by order column", func() {
		position := query.CursorPosition{Keys: keys}
		Expect(position.CursorParams(pageableOrder)).To(Equal(map[string]any{
			"created_at": "2026-01-01T00:00:00Z",
			"id":         "row-9",
		}))
		Expect(query.CursorPosition{}.CursorParams(pageableOrder)).To(BeNil())
	})

	Describe("staleness", func() {
		cursor, _ := query.EncodeCursor(scope, keys, "")

		It("rejects a cursor replayed after a filter changed", func() {
			_, err := query.DecodeCursor(cursor, scopeWith(map[string]any{"tenant": "other"}))
			Expect(err).To(MatchError(query.ErrCursorStale))
			Expect(err).To(MatchError(ContainSubstring("filters changed")))
		})

		It("rejects a cursor replayed after the order changed", func() {
			reordered := scope
			reordered.Order = query.Order{{Column: "updated_at"}, {Column: "id", Unique: true}}
			_, err := query.DecodeCursor(cursor, reordered)
			Expect(err).To(MatchError(query.ErrCursorStale))
			Expect(err).To(MatchError(ContainSubstring("sort order changed")))
		})

		It("rejects a cursor replayed against a different profile", func() {
			other := scope
			other.Profile = "invoices"
			_, err := query.DecodeCursor(cursor, other)
			Expect(err).To(MatchError(query.ErrCursorStale))
		})

		It("rejects a cursor it did not issue", func() {
			_, err := query.DecodeCursor("not-a-cursor!!", scope)
			Expect(err).To(MatchError(query.ErrCursorStale))
		})
	})

	// The page size decides how many rows come back, not which row the position
	// names, so changing it mid-walk must not invalidate an answerable request.
	It("survives a change of page size", func() {
		first := scopeWith(map[string]any{"tenant": "acme", "limit": 50, "offset": 0})
		second := scopeWith(map[string]any{"tenant": "acme", "limit": 200, "offset": 400})

		cursor, err := query.EncodeCursor(first, keys, "")
		Expect(err).ToNot(HaveOccurred())

		position, err := query.DecodeCursor(cursor, second)
		Expect(err).ToNot(HaveOccurred())
		Expect(position.Keys).To(Equal(keys))
	})

	// A keyset profile templates the cursor's own keys into its resume
	// predicate, so the cursor param differs on every page of one walk. Scoping
	// on it would make each cursor invalidate the request it was issued for.
	It("survives its own keys reaching the next request as a param", func() {
		first := scopeWith(map[string]any{"tenant": "acme"})
		second := scopeWith(map[string]any{
			"tenant": "acme",
			"cursor": map[string]any{"created_at": "2026-01-01T00:00:00Z", "id": "row-9"},
		})

		cursor, err := query.EncodeCursor(first, keys, "")
		Expect(err).ToNot(HaveOccurred())

		_, err = query.DecodeCursor(cursor, second)
		Expect(err).ToNot(HaveOccurred())
	})
})
