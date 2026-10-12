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
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore/recordresults"
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
	stats    xetrace.TargetStats
	opened   sqltrace.Opened
	released bool
	served   bool
	dropped  bool
	dropErr  error
}

func (f *fakeXE) Poll(context.Context) (xetrace.TargetSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.released || f.served {
		return xetrace.TargetSnapshot{}, nil
	}
	f.served = true
	return xetrace.TargetSnapshot{Events: f.events, Stats: f.stats}, nil
}

func (f *fakeXE) Drop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropped = true
	return f.dropErr
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
			if xe.opened.Name != "" {
				return xe, xe.opened, nil
			}
			return xe, sqltrace.Opened{Name: "spec"}, nil
		},
		CurrentDatabase: func(context.Context, *sql.DB) (string, error) { return "warehouse", nil },
	}, leaseReleased
}

// hostParams are a host's own form for a capture, which always captures on
// the host's own connection.
type hostParams struct {
	Database string `json:"database" clicky:"title=Database"`
}

func (p hostParams) XEventOptions() (xevent.Options, error) {
	if p.Database == "" {
		return xevent.Options{}, errors.New("database is required")
	}
	return xevent.Options{
		Connection: "oipa",
		Create:     xetrace.CreateOptions{Name: "host_capture", Databases: []string{p.Database}},
		Drain:      xetrace.DrainOptions{Interval: time.Hour},
	}, nil
}

func (p hostParams) Validate() error {
	_, err := p.XEventOptions()
	return err
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

	It("reports the capture's summary, names its session after the XE session, and warns of events it lost", func() {
		xe := &fakeXE{
			events: []xetrace.Event{statement(51, "SELECT 1", 0), statement(52, "SELECT 2", time.Second)},
			stats:  xetrace.TargetStats{DroppedCount: 3},
			opened: sqltrace.Opened{Name: "spec_capture_1", Statements: []string{"CREATE EVENT SESSION spec_capture_1", "ALTER EVENT SESSION spec_capture_1 STATE = START"}},
		}
		kind, _ := capture(xe, nil)
		env := tracestest.NewEnv(map[string]traces.TracePlugin{"sql_xevent": xevent.NewKind(kind)})
		session := env.Start("sql_xevent", params)
		tracestest.Running(session)
		running := session.Snapshot()
		Expect(running.Handle).To(Equal("spec_capture_1"))
		Expect(running.Metadata).To(ConsistOf(query.SessionMetadata{
			Name: "sql_xevent.databases", Label: "Databases", Value: "warehouse",
		}, query.SessionMetadata{
			Name: "sql_xevent.statements", Label: "Started with", Language: "sql",
			Value: "CREATE EVENT SESSION spec_capture_1;\n\nALTER EVENT SESSION spec_capture_1 STATE = START;",
		}))
		xe.release()
		session.Stop("spec")
		info := tracestest.Ended(session)
		Expect(info.Error).To(BeEmpty())
		report := tracestest.Summary(info).Report
		Expect(report).To(HaveKeyWithValue("events", float64(2)))
		Expect(report).To(HaveKeyWithValue("lost", float64(3)))
		Expect(info.Warning).To(ContainSubstring("lost 3 event(s)"))
	})

	It("stores a deadlock report as its decoded graph", func() {
		report := `<deadlock><victim-list><victimProcess id="p1"/></victim-list><process-list><process id="p1" currentdbname="tenant_a"/></process-list><resource-list/></deadlock>`
		xe := &fakeXE{events: []xetrace.Event{{Name: xetrace.EventXMLDeadlockReport, Timestamp: capturedAt, DeadlockReportXML: report}}}
		kind, _ := capture(xe, nil)
		env := tracestest.NewEnv(map[string]traces.TracePlugin{"sql_xevent": xevent.NewKind(kind)})
		session := env.Start("sql_xevent", params)
		tracestest.Running(session)
		xe.release()
		session.Stop("spec")
		rows := env.Rows(tracestest.Ended(session))
		Expect(rows).To(HaveLen(1))
		Expect(rows[0]["deadlock"]).ToNot(BeNil())
		Expect(rows[0]["database"]).To(Equal("tenant_a"))
	})

	It("captures with a host's own params type, through the options it maps them to", func() {
		xe := &fakeXE{events: []xetrace.Event{statement(51, "SELECT 1", 0)}}
		kind, _ := capture(xe, nil)
		var connections []string
		open := kind.Open
		kind.Open = func(ctx dbcontext.Context, connection string) (*sql.DB, func(), error) {
			connections = append(connections, connection)
			return open(ctx, connection)
		}
		host := xevent.NewKindFor[hostParams](kind).WithDefaults(hostParams{Database: "tenant_a"})
		schema, err := host.Params()
		Expect(err).ToNot(HaveOccurred())
		Expect(schema.Properties["database"].Default).To(Equal("tenant_a"))
		Expect(host.ValidateParams(json.RawMessage(`{"database": ""}`))).To(MatchError(ContainSubstring("database is required")))

		env := tracestest.NewEnv(map[string]traces.TracePlugin{"sql_xevent": host})
		session := env.Start("sql_xevent", `{}`)
		tracestest.Running(session)
		xe.release()
		session.Stop("spec")
		info := tracestest.Ended(session)
		Expect(info.Error).To(BeEmpty())
		Expect(env.Rows(info)).To(HaveLen(1))
		Expect(connections).To(Equal([]string{"oipa"}))
	})

	It("records on its session that the XE session could not be dropped, so it is not left running unnoticed", func() {
		xe := &fakeXE{events: []xetrace.Event{statement(51, "SELECT 1", 0)}, dropErr: errors.New("permission denied")}
		kind, leaseReleased := capture(xe, nil)
		env := tracestest.NewEnv(map[string]traces.TracePlugin{"sql_xevent": xevent.NewKind(kind)})
		session := env.Start("sql_xevent", params)
		tracestest.Running(session)
		xe.release()
		session.Stop("spec")
		info := tracestest.Ended(session)
		// A stop the user asked for ends stopped; its error is still recorded.
		Expect(info.State).To(Equal(query.SessionStopped))
		Expect(info.Error).To(ContainSubstring("permission denied"))
		Expect(env.Rows(info)).To(HaveLen(1))
		Expect(env.Sealed(info)).To(BeTrue())
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

var _ = Describe("sql_xevent result profile", func() {
	It("follows a capture's stream and searches its statements' text", func() {
		kind, _ := capture(&fakeXE{}, nil)
		env := tracestest.NewEnv(map[string]traces.TracePlugin{"sql_xevent": xevent.NewKind(kind)})
		profile, err := env.Results.Registry.Get(context.Background(), "traces/sql_xevent")
		Expect(err).ToNot(HaveOccurred())
		Expect(profile.Provider.Type).To(Equal(recordresults.ProviderType))
		var roles []query.ParamRole
		for _, param := range profile.Params {
			roles = append(roles, param.Role)
		}
		Expect(roles).To(ContainElement(query.ParamRoleSearch))
	})
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
