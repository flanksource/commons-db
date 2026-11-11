package sqlite_test

import (
	"fmt"
	"sync"
	"time"

	context "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/providers/sqlite"
	"github.com/flanksource/commons-db/query/sqlactivity"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const sessionPostgresProvider = "session-postgres"

func init() {
	query.RegisterProvider(sqlite.NewSQLProvider(sessionPostgresProvider, models.ConnectionTypePostgres))
}

// sessionRecord is what a run's diagnostics handed back through their
// callbacks.
type sessionRecord struct {
	mu       sync.Mutex
	sessions []query.ServerSession
	samples  []sqlactivity.Sample
	errors   []error
}

func (r *sessionRecord) diagnostics(autoDiagnose time.Duration) *query.ProviderDiagnostics {
	return query.NewDiagnostics(query.DiagnosticOptions{
		Provider: "sql", Walk: true, AutoDiagnose: autoDiagnose,
		OnSession:     func(s query.ServerSession) { r.mu.Lock(); r.sessions = append(r.sessions, s); r.mu.Unlock() },
		OnSample:      func(s sqlactivity.Sample) { r.mu.Lock(); r.samples = append(r.samples, s); r.mu.Unlock() },
		OnSampleError: func(err error) { r.mu.Lock(); r.errors = append(r.errors, err); r.mu.Unlock() },
	})
}

func runRecorded(profile query.Profile, record *sessionRecord, autoDiagnose time.Duration) *query.Result {
	GinkgoHelper()
	result, err := query.Execute(query.WithDiagnosticSink(context.New(), record.diagnostics(autoDiagnose)), profile, nil)
	Expect(err).ToNot(HaveOccurred())
	return result
}

func sessionProfile(provider, dsn, statement string) query.Profile {
	return query.Profile{
		Name:     "session",
		Provider: query.ProviderConfig{Type: provider, Options: map[string]any{"url": dsn}},
		Query:    statement,
	}
}

func postgresProfile(dsn, statement string) query.Profile {
	return sessionProfile(sessionPostgresProvider, dsn, statement)
}

var _ = Describe("server session diagnostics on a live Postgres", Ordered, func() {
	var dsn, database string
	BeforeAll(func(spec SpecContext) {
		db := dbtest.ForGinkgo(dbtest.Options{Name: "sqlsession"})
		dsn = db.DSN()
		Expect(db.SQL().QueryRowContext(spec, "SELECT current_database()").Scan(&database)).To(Succeed())
	})

	DescribeTable("records the backend the statement ran on",
		func(statement string, page int) {
			record := &sessionRecord{}
			result := runRecorded(postgresProfile(dsn, statement), record, 0)
			Expect(result.Rows).To(HaveLen(1))
			Expect(record.sessions).To(HaveLen(1))
			session := record.sessions[0]
			Expect(fmt.Sprint(session.SessionID)).To(Equal(fmt.Sprint(result.Rows[0]["pid"])))
			Expect([]any{session.Engine, session.Database, session.Page}).To(Equal([]any{"postgres", database, page}))
			Expect(record.samples).To(BeEmpty())
		},
		Entry("a paged read", "SELECT pg_backend_pid() AS pid", 1),
		Entry("a program", "SELECT 1 AS warm\nGO\nSELECT pg_backend_pid() AS pid", 0),
	)

	DescribeTable("samples a statement that outruns AutoDiagnose on its own backend",
		func(statement string) {
			record := &sessionRecord{}
			runRecorded(postgresProfile(dsn, statement), record, 200*time.Millisecond)
			Expect(record.errors).To(BeEmpty())
			Expect(record.sessions).To(HaveLen(1))
			Expect(record.samples).ToNot(BeEmpty())
			for _, sample := range record.samples {
				Expect([]any{sample.SessionID, sample.State, sample.Engine}).
					To(Equal([]any{record.sessions[0].SessionID, "active", sqlactivity.EnginePostgres}))
			}
		},
		Entry("a paged read", "SELECT pg_sleep(1)::text AS slept"),
		Entry("a program", "SELECT 1 AS warm\nGO\nSELECT pg_sleep(1)::text AS slept"),
	)

	// A 500ms statement ends long before a 10s AutoDiagnose and before the
	// sampler's 2s interval, so a sample proves only the immediate start.
	DescribeTable("samples from the start when AutoDiagnose is negative, and not at all when it has not passed",
		func(autoDiagnose time.Duration, samples int) {
			record := &sessionRecord{}
			runRecorded(postgresProfile(dsn, "SELECT pg_sleep(0.5)::text AS slept"), record, autoDiagnose)
			Expect(record.errors).To(BeEmpty())
			Expect(record.samples).To(HaveLen(samples))
			for _, sample := range record.samples {
				Expect(sample.SessionID).To(Equal(record.sessions[0].SessionID))
			}
		},
		Entry("negative", time.Duration(-1), 1),
		Entry("not yet due", 10*time.Second, 0),
	)
})

var _ = Describe("server session diagnostics on sqlite", func() {
	DescribeTable("records no session and takes no sample of an in-process database",
		func(statement string) {
			record := &sessionRecord{}
			runRecorded(sessionProfile("sqlite", accountsDB(), statement), record, time.Millisecond)
			Expect([]int{len(record.sessions), len(record.samples), len(record.errors)}).To(Equal([]int{0, 0, 0}))
		},
		Entry("a paged read", "SELECT owner FROM accounts"),
		Entry("a program", "SELECT 1 AS warm\nGO\nSELECT owner FROM accounts"),
	)
})
