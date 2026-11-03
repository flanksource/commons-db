package query_test

import (
	"github.com/flanksource/commons-db/query"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("stateless offset cursors", func() {
	It("round trips the offset alongside typed sort keys", func() {
		scope := scopeWith(nil)
		keys := []any{"2026-11-03", "entry-1"}
		cursor, err := query.EncodeCursor(query.CursorEncoding{Scope: scope, Keys: keys, Offset: 100})
		Expect(err).NotTo(HaveOccurred())
		position, err := query.DecodeCursor(cursor, scope)
		Expect(err).NotTo(HaveOccurred())
		Expect(position).To(Equal(query.CursorPosition{Keys: keys, Offset: 100}))
	})
	DescribeTable("rejects invalid offsets", func(offset int, scroll string) {
		_, err := query.EncodeCursor(query.CursorEncoding{Scope: scopeWith(nil), Keys: []any{"2026-11-03", "entry-1"}, Offset: offset, Scroll: scroll})
		Expect(err).To(HaveOccurred())
	}, Entry("negative", -1, ""), Entry("mixed mechanism", 100, "scroll-1"))
})
