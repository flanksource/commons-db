// Specs for reading a stored event row back as the event it was stored from.
package xesession

import (
	"time"

	"github.com/flanksource/commons-db/tracing/sqlserver/xetrace"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("an event row read back as an event", func() {
	It("is the event it was stored from, its nested statements included", func() {
		child := xetrace.Event{
			Name: xetrace.EventSPStatementCompleted, Statement: "SELECT 1", SQL: "SELECT 1", Duration: 250 * time.Microsecond,
			LogicalReads: 3, ActivityID: "A1", ActivitySeq: 2, Tables: []string{},
		}
		event := xetrace.Event{
			Name: xetrace.EventSQLStatementCompleted, StatementType: "SELECT", Timestamp: capturedAt,
			Duration: 12500 * time.Microsecond, CPUTime: 4 * time.Millisecond, LogicalReads: 120, PhysicalReads: 2,
			Writes: 1, RowCount: 7, DatabaseName: "OMA_DB", ClientApp: "app", ClientHost: "host", Username: "sa", SessionID: 51,
			SQL: "SELECT * FROM AsPolicy", Statement: "SELECT * FROM AsPolicy WHERE 1=1", Tables: []string{"AsPolicy"},
			ErrorNumber: 0, ObjectName: "AsPolicy", ActivityID: "A1", ActivitySeq: 1,
			AdditionalFields: map[string]any{"x": "y"}, Children: []xetrace.Event{child},
		}
		Expect(FromEvent(event).Event()).To(Equal(event))
	})
})
