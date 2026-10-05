// Specs for the sql_xevent trace kind: events an XE session serves are stored
// as sqltrace event rows, through the final drain, and the session is dropped.

package xevent_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/tracing/sqltrace"
	"github.com/flanksource/commons-db/tracing/xetrace"

	"github.com/flanksource/commons-db/tracing/traces"
	"github.com/flanksource/commons-db/tracing/traces/tracestest"
	"github.com/flanksource/commons-db/tracing/traces/xevent"
)

// fakeXE serves its events once they are released, then nothing, and
// records whether it was dropped.
type fakeXE struct {
	mu       sync.Mutex
	events   []xetrace.Event
	released bool
	served   bool
	dropped  bool
}

func (f *fakeXE) Poll(context.Context) (xetrace.TargetSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.released || f.served {
		return xetrace.TargetSnapshot{}, nil
	}
	f.served = true
	return xetrace.TargetSnapshot{Events: f.events}, nil
}

func (f *fakeXE) Drop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropped = true
	return nil
}

func (f *fakeXE) release() {
	f.mu.Lock()
	f.released = true
	f.mu.Unlock()
}

func (f *fakeXE) wasDropped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dropped
}

var capturedAt = time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)

func statement(sid int, text string, offset time.Duration) xetrace.Event {
	return xetrace.Event{
		Name: "sql_statement_completed", SessionID: sid, Statement: text, SQL: text, Username: "sa",
		Timestamp: capturedAt.Add(offset), Duration: time.Millisecond, DatabaseName: "warehouse",
	}
}

// capture is an XE capture over xe, on a stand-in database: the fake session
// never touches it.
func capture(xe *fakeXE, createErr error) (xevent.Capture, *bool) {
	leaseReleased := new(bool)
	return xevent.Capture{
		Open: func(dbcontext.Context, string) (*sql.DB, func(), error) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				return nil, nil, err
			}
			return db, func() { *leaseReleased = true; _ = db.Close() }, nil
		},
		NewSession: func(context.Context, *sql.DB, xetrace.CreateOptions) (sqltrace.XESession, sqltrace.Opened, error) {
			if createErr != nil {
				return nil, sqltrace.Opened{}, createErr
			}
			return xe, sqltrace.Opened{Name: "spec"}, nil
		},
		CurrentDatabase: func(context.Context, *sql.DB) (string, error) { return "warehouse", nil },
	}, leaseReleased
}

const params = `{"connection": "warehouse", "sessionName": "spec", "poll": "1h"}`

var _ = Describe("sql_xevent trace kind", func() {
	It("stores the events its final drain reads as event rows, then drops the XE session", func() {
		xe := &fakeXE{events: []xetrace.Event{
			statement(51, "SELECT * FROM orders", 0),
			statement(52, "CREATE LOGIN app WITH PASSWORD = 'hunter2'", time.Second),
		}}
		kind, leaseReleased := capture(xe, nil)
		env := tracestest.NewEnv(map[string]traces.TracePlugin{"sql_xevent": xevent.NewKind(kind)})
		session := env.Start("sql_xevent", params)
		tracestest.Running(session)
		xe.release()
		session.Stop("spec")
		info := tracestest.Ended(session)
		Expect(info.Error).To(BeEmpty())

		rows := env.Rows(info)
		Expect(rows).To(HaveLen(2))
		Expect(rows[0]["sql"]).To(Equal("SELECT * FROM orders"))
		Expect(rows[0]["sessionId"]).To(BeEquivalentTo(51))
		Expect(rows[0]["username"]).To(Equal("sa"))
		Expect(rows[0]["database"]).To(Equal("warehouse"))
		stored, err := json.Marshal(rows[1])
		Expect(err).ToNot(HaveOccurred())
		Expect(string(stored)).ToNot(ContainSubstring("hunter2"))
		Expect(xe.wasDropped()).To(BeTrue())
		Expect(*leaseReleased).To(BeTrue())
	})

	It("refuses to start when the XE session cannot be created, releasing its lease", func() {
		kind, leaseReleased := capture(&fakeXE{}, errors.New("permission denied"))
		env := tracestest.NewEnv(map[string]traces.TracePlugin{"sql_xevent": xevent.NewKind(kind)})
		_, err := env.Runtime.Start(dbcontext.New(), traces.StartRequest{Kind: "sql_xevent", Params: json.RawMessage(params)})
		Expect(err).To(MatchError(ContainSubstring("permission denied")))
		Expect(*leaseReleased).To(BeTrue())
	})

	DescribeTable("refuses params the capture could not honour",
		func(raw, message string) {
			kind, _ := capture(&fakeXE{}, nil)
			Expect(xevent.NewKind(kind).ValidateParams(json.RawMessage(raw))).To(MatchError(ContainSubstring(message)))
		},
		Entry("no connection", `{"sessionName": "spec"}`, "connection"),
		Entry("no session name", `{"connection": "warehouse"}`, "sessionName"),
		Entry("a negative minimum duration", `{"connection": "warehouse", "sessionName": "spec", "minDuration": "-1s"}`, "minDuration"),
		Entry("an option it does not define", `{"connection": "warehouse", "sessionName": "spec", "minDurationMs": 5}`, "minDurationMs"),
	)
})

var _ = Describe("sql_xevent params form", func() {
	It("lists the connection and every capture option as fields of their own", func() {
		schema, err := xevent.Kind().Params()
		Expect(err).ToNot(HaveOccurred())
		Expect(schema.Properties).To(HaveKey("connection"))
		Expect(schema.Properties).To(HaveKey("sessionName"))
		Expect(schema.Properties).To(HaveKey("minDuration"))
	})
})
