// Specs for the SQL statement tap: observers of a connection, or of every
// connection, receive each statement published while they observe.

package connection

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL statement tap", func() {
	It("hands a statement to the observers of its connection and to those of every connection", func() {
		var events, all, other []string
		DeferCleanup(ObserveSQL("events-db", func(s Statement) { events = append(events, s.SQL) }))
		DeferCleanup(ObserveSQL("", func(s Statement) { all = append(all, s.SQL) }))
		DeferCleanup(ObserveSQL("other-db", func(s Statement) { other = append(other, s.SQL) }))

		Expect(ObservingSQL("events-db")).To(BeTrue())
		PublishSQL(Statement{Connection: "events-db", SQL: "SELECT 1"})
		PublishSQL(Statement{SQL: "SELECT 2"})

		Expect(events).To(Equal([]string{"SELECT 1"}))
		Expect(all).To(Equal([]string{"SELECT 1", "SELECT 2"}))
		Expect(other).To(BeEmpty())
	})

	It("stops once released, and a second release is harmless", func() {
		var seen []string
		release := ObserveSQL("events-db", func(s Statement) { seen = append(seen, s.SQL) })
		release()
		release()

		Expect(ObservingSQL("events-db")).To(BeFalse())
		PublishSQL(Statement{Connection: "events-db", SQL: "SELECT 1"})
		Expect(seen).To(BeEmpty())
	})
})
