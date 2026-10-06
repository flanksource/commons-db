// Specs for the SQL statement tap: observers of a connection, of every named
// connection, or of the context's own database receive what is published for it.

package connection

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SQL statement tap", func() {
	It("hands a statement to the observers of its connection and to those of every named connection", func() {
		var events, every, own, other []string
		DeferCleanup(ObserveSQL("events-db", func(s Statement) { events = append(events, s.SQL) }))
		DeferCleanup(ObserveSQL(EveryConnection, func(s Statement) { every = append(every, s.SQL) }))
		DeferCleanup(ObserveSQL("", func(s Statement) { own = append(own, s.SQL) }))
		DeferCleanup(ObserveSQL("other-db", func(s Statement) { other = append(other, s.SQL) }))

		Expect(ObservingSQL("events-db")).To(BeTrue())
		PublishSQL(Statement{Connection: "events-db", SQL: "SELECT 1"})
		PublishSQL(Statement{SQL: "SELECT 2"})

		Expect(events).To(Equal([]string{"SELECT 1"}))
		Expect(every).To(Equal([]string{"SELECT 1"}))
		Expect(own).To(Equal([]string{"SELECT 2"}))
		Expect(other).To(BeEmpty())
	})

	It("does not count observers of every named connection as observers of the context's own database", func() {
		DeferCleanup(ObserveSQL(EveryConnection, func(Statement) {}))

		Expect(ObservingSQL("events-db")).To(BeTrue())
		Expect(ObservingSQL("")).To(BeFalse())
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
