package query_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"

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
		Profile:  "orders",
		Provider: "clickhouse",
		Query:    "SELECT created_at, id FROM orders",
		Order:    pageableOrder,
		Params:   params,
		Roles: map[string]query.ParamRole{
			"tenant": query.ParamRoleFilter,
			"limit":  query.ParamRoleLimit,
			"offset": query.ParamRoleOffset,
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
		cursor, err := query.EncodeCursor(scope, keys, "pit-1", nil)
		Expect(err).ToNot(HaveOccurred())

		position, err := query.DecodeCursor(cursor, scope)
		Expect(err).ToNot(HaveOccurred())
		Expect(position.Keys).To(Equal(keys))
		Expect(position.PIT).To(Equal("pit-1"))
	})

	It("round-trips unsigned keys above JSON's exact integer range", func() {
		large := uint64(1<<63 + 17)
		cursor, err := query.EncodeCursor(scope, []any{"2026-01-01T00:00:00Z", large}, "", nil)
		Expect(err).ToNot(HaveOccurred())

		position, err := query.DecodeCursor(cursor, scope)
		Expect(err).ToNot(HaveOccurred())
		Expect(position.Keys).To(Equal([]any{"2026-01-01T00:00:00Z", large}))
	})

	// A processor that folds rows across pages is only correct page by page if
	// it can remember what it already emitted, and the cursor is the only thing
	// a resumed request brings with it.
	It("carries each processor's state back to the page that resumes", func() {
		state := map[string][]byte{"cel.dedupe": {0x01, 0x02}, "logs.parse": {0xff}}
		cursor, err := query.EncodeCursor(scope, keys, "", state)
		Expect(err).ToNot(HaveOccurred())

		position, err := query.DecodeCursor(cursor, scope)
		Expect(err).ToNot(HaveOccurred())
		Expect(position.State).To(Equal(state))
	})

	It("carries a compressible page processor state without exceeding the transport ceiling", func() {
		state := map[string][]byte{
			"java.stacktrace": bytes.Repeat([]byte("\tat com.acme.billing.InvoiceJob.run(InvoiceJob.java:64)\n"), 200),
		}
		cursor, err := query.EncodeCursor(scope, keys, "", state)
		Expect(err).ToNot(HaveOccurred())
		Expect(len(cursor)).To(BeNumerically("<=", query.MaxCursorBytes))

		position, err := query.DecodeCursor(cursor, scope)
		Expect(err).ToNot(HaveOccurred())
		Expect(position.State).To(Equal(state))
	})

	It("refuses to issue a cursor larger than one can be carried in", func() {
		stateBytes := make([]byte, query.MaxCursorBytes*4)
		block := make([]byte, 8)
		for offset := 0; offset < len(stateBytes); offset += sha256.Size {
			binary.BigEndian.PutUint64(block, uint64(offset))
			digest := sha256.Sum256(block)
			copy(stateBytes[offset:], digest[:])
		}
		state := map[string][]byte{"cel.dedupe": stateBytes}
		_, err := query.EncodeCursor(scope, keys, "", state)
		Expect(err).To(MatchError(ContainSubstring("no longer fits in a cursor")))
		Expect(err).To(MatchError(ContainSubstring("cel.dedupe")))
	})

	// A cursor holds one key per ordered column; anything else would resume from
	// a position the order cannot locate.
	It("refuses to issue a cursor whose keys do not match the order", func() {
		_, err := query.EncodeCursor(scope, []any{"only-one"}, "", nil)
		Expect(err).To(MatchError(ContainSubstring("one key per order column")))
	})

	It("refuses to issue a cursor for an order that cannot be paged", func() {
		unordered := scope
		unordered.Order = query.Order{{Column: "created_at"}}
		_, err := query.EncodeCursor(unordered, []any{"x"}, "", nil)
		Expect(err).To(MatchError(ContainSubstring("not declared unique")))
	})

	Describe("staleness", func() {
		cursor, _ := query.EncodeCursor(scope, keys, "", nil)

		// Cursors issued before compressed transport must not be mistaken for the
		// current format.
		It("rejects an uncompressed cursor from an older transport", func() {
			payload, err := json.Marshal(map[string]any{
				"v": 2, "n": "logs", "k": []map[string]string{{"t": "string", "v": "row-9"}},
			})
			Expect(err).ToNot(HaveOccurred())
			stale := query.Cursor(base64.RawURLEncoding.EncodeToString(payload))

			_, err = query.DecodeCursor(stale, scope)
			Expect(err).To(MatchError(query.ErrCursorStale))
			Expect(err).To(MatchError(ContainSubstring("not a cursor this server issued")))
		})

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

		It("rejects a cursor after the rendered provider query changes", func() {
			changed := scope
			changed.Query = "SELECT created_at, id FROM archived_orders"
			_, err := query.DecodeCursor(cursor, changed)
			Expect(err).To(MatchError(query.ErrCursorStale))
			Expect(err).To(MatchError(ContainSubstring("query inputs changed")))
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

		cursor, err := query.EncodeCursor(first, keys, "", nil)
		Expect(err).ToNot(HaveOccurred())

		position, err := query.DecodeCursor(cursor, second)
		Expect(err).ToNot(HaveOccurred())
		Expect(position.Keys).To(Equal(keys))
	})

})
