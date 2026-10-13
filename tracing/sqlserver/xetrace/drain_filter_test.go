// Specifies which events CreateOptions.DrainFilter scopes by database in Go,
// because the XE session predicate cannot.
package xetrace

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CreateOptions.DrainFilter", func() {
	It("leaves the filter alone when the session predicate scopes every event", func() {
		opts := CreateOptions{Databases: []string{"warehouse"}, Events: DefaultEvents, Filter: EventFilter{Tables: []string{"Orders"}}}

		Expect(opts.DrainFilter()).To(Equal(EventFilter{Tables: []string{"Orders"}}))
	})

	It("scopes system_health's events, which no predicate of ours reaches", func() {
		opts := CreateOptions{Session: SystemHealthSession, Databases: []string{"warehouse"}}

		Expect(opts.DrainFilter().Databases).To(Equal([]string{"warehouse"}))
	})

	It("keeps a database filter the caller set", func() {
		opts := CreateOptions{
			Databases: []string{"warehouse"}, Events: []string{EventXMLDeadlockReport},
			Filter: EventFilter{Databases: []string{"warehouse_uat"}},
		}

		Expect(opts.DrainFilter().Databases).To(Equal([]string{"warehouse_uat"}))
	})
})
