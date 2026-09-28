package xetrace

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const deadlockEventXML = `<event name="xml_deadlock_report" timestamp="2026-09-24T10:11:12.345Z"><data name="xml_report"><value><deadlock><victim-list><victimProcess id="p1"/></victim-list><process-list><process id="p1" currentdbname="tenant_a"/><process id="p2" currentdbname="tenant_b"/></process-list><resource-list/></deadlock></value></data></event>`

var _ = Describe("xml_deadlock_report capture", func() {
	It("keeps the instance-level event free of statement predicates", func() {
		sql, err := BuildCreateSQL(CreateOptions{
			Name: "test_trace", Events: []string{EventSQLStatementCompleted, EventXMLDeadlockReport},
			Databases: []string{"tenant_b"}, Users: []string{"app_user"}, MinDurationMicros: 1000,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(sql).To(ContainSubstring("ADD EVENT sqlserver.xml_deadlock_report"))
		deadlockClause := sql[strings.Index(sql, "ADD EVENT sqlserver.xml_deadlock_report"):]
		Expect(deadlockClause).NotTo(ContainSubstring("WHERE"))
		Expect(sql).To(ContainSubstring("sqlserver.database_name"))
	})

	It("preserves a nested report from both XE targets and matches any participant database", func() {
		raw, err := parseEventDataRow(deadlockEventXML)
		Expect(err).NotTo(HaveOccurred())
		fileEvents, _ := collectEvents([]rawXMLEvent{raw})
		ring, err := ParseRingBuffer("<RingBufferTarget>" + deadlockEventXML + "</RingBufferTarget>")
		Expect(err).NotTo(HaveOccurred())
		Expect(ring.Events).To(Equal(fileEvents))
		Expect(fileEvents).To(HaveLen(1))
		Expect(fileEvents[0].DeadlockReportXML).To(ContainSubstring(`<process id="p2" currentdbname="tenant_b"/>`))
		Expect(EventFilter{Databases: []string{"tenant_b"}}.match(fileEvents[0])).To(BeTrue())
		Expect(EventFilter{Databases: []string{"tenant_c"}}.match(fileEvents[0])).To(BeFalse())
		Expect(EventFilter{Databases: []string{"tenant_b"}, Users: []string{"missing"}, Apps: []string{"missing"}, Types: []string{"DML"}, MinDuration: time.Hour}.match(fileEvents[0])).To(BeTrue())
	})

	It("rejects malformed reports before a database filter can hide them", func() {
		bad := `<event name="xml_deadlock_report" timestamp="2026-09-24T10:11:12.345Z"><data name="xml_report"><value><deadlock/></value></data></event>`
		_, err := parseEventDataRow(bad)
		Expect(err).To(MatchError(ContainSubstring("lists no processes")))
		_, err = ParseRingBuffer("<RingBufferTarget>" + bad + "</RingBufferTarget>")
		Expect(err).To(MatchError(ContainSubstring("lists no processes")))
	})
})
