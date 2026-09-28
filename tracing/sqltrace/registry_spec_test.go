package sqltrace

import (
	"context"
	"errors"
	"time"

	"database/sql"
	clickycache "github.com/flanksource/clicky/cache"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/tracing/xetrace"
)

var _ = ginkgo.Describe("Registry", func() {
	var env environment

	ginkgo.BeforeEach(func() { env = newEnvironment(nil) })

	ginkgo.It("appends every captured row to the environment's sqlite stream exactly once, then seals it", func() {
		first, second, third := statement(10, "SELECT 1", 0), statement(11, "SELECT 2", time.Second), statement(12, "SELECT 3", 2*time.Second)
		// The second poll re-delivers the first poll's newest event, as a ring
		// buffer read does, and the final drain on stop reads the buffer again.
		xe := &fakeXE{batches: [][]xetrace.Event{{first, second}, {second, third}}}
		registry := newTestRegistry(env.store(), xe)

		trace, err := registry.Start(env.ctx(), StartOptions{Poll: 5 * time.Millisecond})
		Expect(err).ToNot(HaveOccurred())
		Eventually(func() int64 { return trace.EventsRef().High }).Should(Equal(int64(3)))
		_, err = registry.Stop(trace.ID)
		Expect(err).ToNot(HaveOccurred())

		Expect(env.statements(trace.ID)).To(Equal([]string{"SELECT 1", "SELECT 2", "SELECT 3"}))
		meta := env.meta(trace.ID)
		Expect(meta.Total).To(Equal(int64(3)))
		Expect(meta.Sealed).To(BeTrue())
		Expect(xe.wasDropped()).To(BeTrue())
	})

	// A step shorter than the poll interval is only observed if Stop waits for
	// the final drain, and a follower only stops waiting once the stream is sealed.
	ginkgo.It("commits the final drain on stop before the stream is sealed", func() {
		xe := &fakeXE{batches: [][]xetrace.Event{{statement(20, "SELECT short", 0)}}}
		registry := newTestRegistry(env.store(), xe)

		trace, err := registry.Start(env.ctx(), StartOptions{Poll: time.Hour})
		Expect(err).ToNot(HaveOccurred())
		Expect(env.meta(trace.ID).Sealed).To(BeFalse())
		Expect(trace.Done()).ToNot(BeClosed())

		_, err = registry.Stop(trace.ID)
		Expect(err).ToNot(HaveOccurred())

		Expect(trace.Done()).To(BeClosed())
		Expect(env.statements(trace.ID)).To(Equal([]string{"SELECT short"}))
		Expect(env.meta(trace.ID).Sealed).To(BeTrue())
		ref, err := trace.Checkpoint(context.Background())
		Expect(err).ToNot(HaveOccurred())
		Expect([]int64{ref.From, ref.To, ref.Total}).To(Equal([]int64{1, 1, 1}))
	})

	// A web start's request ends long before its capture does: the rows must
	// still reach the environment that request named.
	ginkgo.It("keeps routing rows to the starting request's environment after that request is cancelled", func() {
		xe := &fakeXE{batches: [][]xetrace.Event{nil, {statement(25, "SELECT later", 0)}}}
		registry := newTestRegistry(env.store(), xe)
		request, cancel := context.WithCancel(env.ctx())

		trace, err := registry.Start(request, StartOptions{Poll: 5 * time.Millisecond})
		Expect(err).ToNot(HaveOccurred())
		cancel()
		Eventually(func() int64 { return trace.EventsRef().High }).Should(Equal(int64(1)))
		_, err = registry.Stop(trace.ID)
		Expect(err).ToNot(HaveOccurred())

		Expect(env.statements(trace.ID)).To(Equal([]string{"SELECT later"}))
	})

	ginkgo.It("fails the capture visibly when the record store refuses an append", func() {
		refused := errors.New("database or disk is full")
		gate := &gatedStore{RecordStore: env.store(), release: make(chan struct{}), failed: refused}
		close(gate.release)
		xe := &fakeXE{batches: [][]xetrace.Event{{statement(30, "SELECT lost", 0)}}}
		registry := newTestRegistry(gate, xe)

		trace, err := registry.Start(env.ctx(), StartOptions{Poll: 5 * time.Millisecond})
		Expect(err).ToNot(HaveOccurred())

		Eventually(trace.Running).Should(BeFalse(), "a store failure stops the capture rather than letting it run unrecorded")
		Expect(trace.Err()).To(MatchError(ContainSubstring("database or disk is full")))
		Expect(xe.wasDropped()).To(BeTrue())
		_, err = trace.Checkpoint(context.Background())
		Expect(err).To(MatchError(refused))
		_, err = registry.Stop(trace.ID)
		Expect(err).To(MatchError(ContainSubstring("database or disk is full")))
	})

	ginkgo.It("never writes a sql-trace: key into the environment's cache", func() {
		spy := &spyStore{Store: clickycache.NewMemory()}
		env = newEnvironment(spy)
		xe := &fakeXE{batches: [][]xetrace.Event{{statement(40, "SELECT kv", 0)}}}
		registry := newTestRegistry(env.store(), xe)

		trace, err := registry.Start(env.ctx(), StartOptions{Poll: 5 * time.Millisecond})
		Expect(err).ToNot(HaveOccurred())
		_, err = registry.Stop(trace.ID)
		Expect(err).ToNot(HaveOccurred())

		Expect(env.statements(trace.ID)).To(Equal([]string{"SELECT kv"}))
		keys := spy.written()
		Expect(keys).ToNot(BeEmpty(), "the spy must see the capture's writes for its silence on sql-trace: to mean anything")
		Expect(containingAny(keys, "sql-trace:")).To(BeEmpty())
		Expect(keys).To(HaveEach(HavePrefix("lab:trace-results")))
	})

	ginkgo.It("holds one database lease from start until the final drain", func() {
		var leases, releases int
		registry, err := NewRegistry(RegistryOptions{
			DB: func(context.Context) (*sql.DB, func(), error) {
				leases++
				return nil, func() { releases++ }, nil
			},
			Store: env.store(),
			NewSession: func(context.Context, *sql.DB, xetrace.CreateOptions) (XESession, Opened, error) {
				return &fakeXE{}, Opened{Name: "lease-test"}, nil
			},
			CurrentDatabase: func(context.Context, *sql.DB) (string, error) { return "warehouse", nil },
		})
		Expect(err).ToNot(HaveOccurred())

		trace, err := registry.Start(env.ctx(), StartOptions{Poll: time.Hour})
		Expect(err).ToNot(HaveOccurred())
		Expect([]int{leases, releases}).To(Equal([]int{1, 0}))

		_, err = registry.Stop(trace.ID)
		Expect(err).ToNot(HaveOccurred())
		Expect([]int{leases, releases}).To(Equal([]int{1, 1}))
	})

	ginkgo.DescribeTable("scopes the server-wide session to the databases asked for",
		func(databases, wantPredicate []string, wantDatabase string) {
			batch := []xetrace.Event{statement(1, "SELECT warehouse", 0), statement(2, "SELECT archive", time.Second), statement(3, "SELECT master", 2*time.Second)}
			batch[1].DatabaseName, batch[2].DatabaseName = "warehouse_archive", "master"
			xe := &fakeXE{batches: [][]xetrace.Event{batch}}
			var created xetrace.CreateOptions
			registry, err := NewRegistry(RegistryOptions{
				DB:    func(context.Context) (*sql.DB, func(), error) { return nil, func() {}, nil },
				Store: env.store(),
				NewSession: func(_ context.Context, _ *sql.DB, opts xetrace.CreateOptions) (XESession, Opened, error) {
					created = opts
					return xe, Opened{Name: "scope-test"}, nil
				},
				CurrentDatabase: func(context.Context, *sql.DB) (string, error) { return "warehouse", nil },
			})
			Expect(err).ToNot(HaveOccurred())

			trace, err := registry.Start(env.ctx(), StartOptions{
				CreateOptions: xetrace.CreateOptions{Databases: databases}, Poll: time.Hour,
			})
			Expect(err).ToNot(HaveOccurred())
			_, err = registry.Stop(trace.ID)
			Expect(err).ToNot(HaveOccurred())

			Expect(created.Databases).To(Equal(wantPredicate))
			Expect(trace.Database).To(Equal(wantDatabase))
			Expect(env.statements(trace.ID)).To(Equal([]string{"SELECT warehouse", "SELECT archive", "SELECT master"}))
		},
		// The fake delivers every database: every pattern set is the server's to
		// apply, and the engine must not filter a second time on top of it.
		ginkgo.Entry("unset keeps the context's database", nil, []string{"warehouse"}, "warehouse"),
		ginkgo.Entry("one plain name is pushed into the session predicate", []string{"warehouse_archive"}, []string{"warehouse_archive"}, "warehouse_archive"),
		ginkgo.Entry("patterns are pushed into the session predicate",
			[]string{"warehouse*", "!warehouse_archive"}, []string{"warehouse*", "!warehouse_archive"}, "warehouse*, !warehouse_archive"),
		ginkgo.Entry("* reaches the session as every database", []string{"*"}, []string{"*"}, "*"),
	)

	ginkgo.It("applies the selected database to managed deadlock graphs after capture", func() {
		var created xetrace.CreateOptions
		registry, err := NewRegistry(RegistryOptions{
			DB:    func(context.Context) (*sql.DB, func(), error) { return nil, func() {}, nil },
			Store: env.store(),
			NewSession: func(_ context.Context, _ *sql.DB, opts xetrace.CreateOptions) (XESession, Opened, error) {
				created = opts
				return &fakeXE{}, Opened{Name: "deadlock-scope"}, nil
			},
			CurrentDatabase: func(context.Context, *sql.DB) (string, error) { return "tenant_a", nil },
		})
		Expect(err).NotTo(HaveOccurred())
		trace, err := registry.Start(env.ctx(), StartOptions{
			CreateOptions: xetrace.CreateOptions{Events: []string{xetrace.EventXMLDeadlockReport}, Databases: []string{"tenant_b"}},
			Poll:          time.Hour,
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = registry.Stop(trace.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(created.Filter.Databases).To(Equal([]string{"tenant_b"}))
	})

	ginkgo.It("passes the full option set through to the XE session factory and keeps what it opened", func() {
		opts := StartOptions{
			CreateOptions: xetrace.CreateOptions{
				Users: []string{"app", "!sa"}, MinDurationMicros: 250_000,
				Events: []string{xetrace.EventSQLStatementCompleted, xetrace.EventRPCCompleted},
				Apps:   []string{"!go/reporting"}, Hosts: []string{"app-0"},
				MaxMemoryKB: 8192, MaxEvents: 5000,
				Filter:    xetrace.EventFilter{Types: []string{"SELECT"}, Tables: []string{"As*"}},
				Databases: []string{"warehouse"},
			},
			Duration: 5 * time.Minute,
			Poll:     time.Hour,
		}
		var seen xetrace.CreateOptions
		opened := Opened{Name: "options-test", Statements: []string{
			"CREATE EVENT SESSION [options-test] ON SERVER ADD EVENT sqlserver.rpc_completed",
			"ALTER EVENT SESSION [options-test] ON SERVER STATE = START",
		}}
		registry, err := NewRegistry(RegistryOptions{
			DB:    func(context.Context) (*sql.DB, func(), error) { return nil, func() {}, nil },
			Store: env.store(),
			NewSession: func(_ context.Context, _ *sql.DB, o xetrace.CreateOptions) (XESession, Opened, error) {
				seen = o
				return &fakeXE{}, opened, nil
			},
			CurrentDatabase: func(context.Context, *sql.DB) (string, error) { return "warehouse", nil },
		})
		Expect(err).ToNot(HaveOccurred())

		trace, err := registry.Start(env.ctx(), opts)
		Expect(err).ToNot(HaveOccurred())
		ginkgo.DeferCleanup(func() { _, _ = registry.Stop(trace.ID) })

		Expect(seen).To(Equal(opts.CreateOptions))
		Expect(trace.SessionName).To(Equal(opened.Name))
		Expect(trace.Statements).To(Equal(opened.Statements))
		Expect(trace.StopAt).To(BeTemporally("~", trace.StartedAt.Add(5*time.Minute), time.Second))
	})

	ginkgo.It("keeps the summary identical to a full re-summary of the stored events, counting what it missed", func() {
		metered := func(sid int, sql, table string, offset time.Duration, reads int64) xetrace.Event {
			e := statement(sid, sql, offset)
			e.CPUTime, e.LogicalReads, e.RowCount, e.Tables = 500*time.Microsecond, reads, 3, []string{table}
			return e
		}
		kept := []xetrace.Event{
			metered(10, "SELECT 1 FROM AsActivity", "AsActivity", 0, 40),
			metered(12, "SELECT 3 FROM AsActivity", "AsActivity", 2*time.Second, 100),
		}
		xe := &fakeXE{
			batches: [][]xetrace.Event{
				{kept[0], metered(11, "SELECT 2 FROM AsPolicy", "AsPolicy", time.Second, 60)},
				{kept[1], statement(13, "exec sp_execute 91,1", 3*time.Second)},
			},
			stats: []xetrace.TargetStats{{DroppedCount: 3}},
		}
		registry := newTestRegistry(env.store(), xe)

		trace, err := registry.Start(env.ctx(), StartOptions{
			CreateOptions: xetrace.CreateOptions{Filter: xetrace.EventFilter{Tables: []string{"AsActivity"}}},
			Poll:          5 * time.Millisecond,
		})
		Expect(err).ToNot(HaveOccurred())
		Eventually(func() int64 { return trace.EventsRef().High }).Should(Equal(int64(2)))
		_, err = registry.Stop(trace.ID)
		Expect(err).ToNot(HaveOccurred())

		want := xetrace.Summarize(kept)
		want.Lost, want.Unresolved = 3, 1
		Expect(trace.Summary()).To(Equal(want))
		Expect(trace.Result().Summary).To(Equal(&want))
	})

	ginkgo.It("stops and drops every trace it started", func() {
		sessions := []*fakeXE{{}, {}}
		calls := 0
		registry, err := NewRegistry(RegistryOptions{
			DB:    func(context.Context) (*sql.DB, func(), error) { return nil, func() {}, nil },
			Store: env.store(),
			NewSession: func(context.Context, *sql.DB, xetrace.CreateOptions) (XESession, Opened, error) {
				session := sessions[calls]
				calls++
				return session, Opened{Name: "stop-all"}, nil
			},
			CurrentDatabase: func(context.Context, *sql.DB) (string, error) { return "warehouse", nil },
		})
		Expect(err).ToNot(HaveOccurred())
		first, err := registry.Start(env.ctx(), StartOptions{Poll: 5 * time.Millisecond})
		Expect(err).ToNot(HaveOccurred())
		second, err := registry.Start(env.ctx(), StartOptions{Poll: 5 * time.Millisecond})
		Expect(err).ToNot(HaveOccurred())

		registry.StopAll()

		Expect([]bool{sessions[0].wasDropped(), sessions[1].wasDropped()}).To(Equal([]bool{true, true}))
		Expect([]bool{env.meta(first.ID).Sealed, env.meta(second.ID).Sealed}).To(Equal([]bool{true, true}))
	})
})
