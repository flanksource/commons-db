package sqltrace

import (
	"context"
	"errors"
	"fmt"
	"time"

	"database/sql"
	"github.com/flanksource/commons/properties"
	mssql "github.com/microsoft/go-mssqldb"

	"github.com/flanksource/commons-db/tracing/xetrace"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("SQL trace errors", func() {
	var env environment

	ginkgo.BeforeEach(func() { env = newEnvironment(nil) })

	// "ring buffer denied" is not in xetrace's transient allowlist, so it is
	// terminal on the first tick — which is what keeps this spec fast. If it
	// were ever classified transient, the retry budget (2 attempts x a 2s pause)
	// would blow Eventually's 1s default and this would flake rather than fail.
	ginkgo.It("records a poll failure, marks the trace stopped and still seals its stream", func() {
		registry := newTestRegistry(env.store(), &fakeXE{pollErr: []error{errors.New("ring buffer denied")}})
		trace, err := registry.Start(env.ctx(), StartOptions{Poll: time.Millisecond})
		Expect(err).NotTo(HaveOccurred())

		Eventually(trace.Running).Should(BeFalse())
		Expect(trace.Err()).To(MatchError(ContainSubstring("ring buffer denied")))
		Expect(trace.StoppedAt).NotTo(BeZero())
		Expect(env.meta(trace.ID).Sealed).To(BeTrue())
		_, err = registry.Stop(trace.ID)
		Expect(err).To(MatchError(ContainSubstring("ring buffer denied")))
	})

	ginkgo.It("aborts immediately when the event session has been dropped externally", func() {
		gone := fmt.Errorf("%w: %q", xetrace.ErrSessionGone, "commons_db_trace_1_2")
		registry := newTestRegistry(env.store(), &fakeXE{pollErr: []error{gone}})
		trace, err := registry.Start(env.ctx(), StartOptions{Poll: time.Millisecond})
		Expect(err).NotTo(HaveOccurred())

		// Retrying a vanished session can never produce data, so it must fail
		// fast with a message that names the cause rather than a retry count.
		Eventually(trace.Running).Should(BeFalse())
		Expect(trace.Err()).To(MatchError(ContainSubstring("no longer present in sys.dm_xe_sessions")))
	})

	ginkgo.It("keeps capturing when a poll failure is transient", func() {
		properties.Set("sqltrace.poll.retryDelay", "1ms")
		ginkgo.DeferCleanup(func() { properties.Set("sqltrace.poll.retryDelay", "") })

		transient := mssql.StreamError{InnerError: fmt.Errorf(
			"did not get cancellation confirmation from the server (current response: %w)", context.DeadlineExceeded)}
		xe := &fakeXE{pollErr: []error{fmt.Errorf("read ring_buffer target: %w", transient), nil}}
		registry := newTestRegistry(env.store(), xe)

		trace, err := registry.Start(env.ctx(), StartOptions{Poll: time.Millisecond})
		Expect(err).NotTo(HaveOccurred())

		// A recovered blip must leave no terminal error behind: the trace is
		// still live, and Stop reports success.
		Consistently(trace.Err, 100*time.Millisecond).Should(Succeed())
		Expect(trace.Running()).To(BeTrue())
		_, err = registry.Stop(trace.ID)
		Expect(err).NotTo(HaveOccurred())
	})

	ginkgo.It("propagates session cleanup failures", func() {
		registry := newTestRegistry(env.store(), &fakeXE{dropErr: errors.New("drop denied")})
		trace, err := registry.Start(env.ctx(), StartOptions{Poll: time.Hour})
		Expect(err).NotTo(HaveOccurred())

		_, err = registry.Stop(trace.ID)
		Expect(err).To(MatchError(ContainSubstring("drop denied")))
		Expect(trace.Err()).To(MatchError(ContainSubstring("drop denied")))
	})

	ginkgo.It("drops the XE session when the stream cannot be opened", func() {
		xe := &fakeXE{}
		registry := newTestRegistry(env.store(), xe)

		// No environment runtime: the router cannot name the store to open.
		_, err := registry.Start(context.Background(), StartOptions{})

		Expect(err).To(MatchError(ContainSubstring("open sql_xevent stream")))
		Expect(xe.wasDropped()).To(BeTrue(), "a session nothing records must not stay on the server")
	})

	ginkgo.It("reports an unknown trace id on Stop", func() {
		registry := newTestRegistry(env.store(), &fakeXE{})

		_, err := registry.Stop("missing")

		Expect(err).To(MatchError(`trace "missing" not found`))
	})

	ginkgo.DescribeTable("refuses a registry missing a seam it needs",
		func(mutate func(*RegistryOptions), message string) {
			opts := RegistryOptions{
				DB:    func(context.Context) (*sql.DB, func(), error) { return nil, func() {}, nil },
				Store: env.store(),
				NewSession: func(context.Context, *sql.DB, xetrace.CreateOptions) (XESession, Opened, error) {
					return &fakeXE{}, Opened{}, nil
				},
				CurrentDatabase: func(context.Context, *sql.DB) (string, error) { return "warehouse", nil },
			}
			mutate(&opts)

			_, err := NewRegistry(opts)

			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		ginkgo.Entry("database provider", func(o *RegistryOptions) { o.DB = nil }, "database provider"),
		ginkgo.Entry("record store", func(o *RegistryOptions) { o.Store = nil }, "record store"),
		ginkgo.Entry("session factory", func(o *RegistryOptions) { o.NewSession = nil }, "session factory"),
		ginkgo.Entry("database resolver", func(o *RegistryOptions) { o.CurrentDatabase = nil }, "database resolver"),
	)

	ginkgo.It("refuses a database lease with no release", func() {
		registry, err := NewRegistry(RegistryOptions{
			DB:    func(context.Context) (*sql.DB, func(), error) { return nil, nil, nil },
			Store: env.store(),
			NewSession: func(context.Context, *sql.DB, xetrace.CreateOptions) (XESession, Opened, error) {
				return &fakeXE{}, Opened{}, nil
			},
			CurrentDatabase: func(context.Context, *sql.DB) (string, error) { return "warehouse", nil },
		})
		Expect(err).NotTo(HaveOccurred())

		_, err = registry.Start(env.ctx(), StartOptions{})

		Expect(err).To(MatchError(ContainSubstring("no release")))
	})
})
