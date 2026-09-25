package sqltrace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/commons/properties"
	mssql "github.com/microsoft/go-mssqldb"

	"github.com/flanksource/commons-db/tracing/xetrace"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type failingXE struct {
	mu sync.Mutex
	// pollErrs are returned in order; the last one repeats once exhausted. A
	// nil entry yields a successful empty poll.
	pollErrs []error
	calls    int
	dropErr  error
}

func (f *failingXE) Poll(context.Context) (xetrace.RingBufferSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pollErrs) == 0 {
		return xetrace.RingBufferSnapshot{}, nil
	}
	err := f.pollErrs[len(f.pollErrs)-1]
	if f.calls < len(f.pollErrs) {
		err = f.pollErrs[f.calls]
	}
	f.calls++
	return xetrace.RingBufferSnapshot{}, err
}

func (f *failingXE) Drop(context.Context) error {
	return f.dropErr
}

func (f *failingXE) Database() string { return "testdb" }

func failingWithPollErr(err error) *failingXE { return &failingXE{pollErrs: []error{err}} }

func registryWithXE(xe xeSession) *Registry {
	return &Registry{
		traces:  make(map[string]*ActiveTrace),
		dbFor:   func(context.Context) (*sql.DB, func(), error) { return nil, func() {}, nil },
		events:  &memoryLog{chunks: map[string][][]xetrace.Event{}},
		nowFunc: func() time.Time { return time.Now().UTC() },
		xeFactory: func(context.Context, *sql.DB, xetrace.CreateOptions) (xeSession, string, error) {
			return xe, "error-session", nil
		},
	}
}

var _ = ginkgo.Describe("SQL trace errors", func() {
	ginkgo.It("returns a structured forbidden response for missing XEvent permissions", func() {
		r := registryWithXE(nil)
		r.xeFactory = func(context.Context, *sql.DB, xetrace.CreateOptions) (xeSession, string, error) {
			return nil, "", &xetrace.PermissionError{Report: xetrace.PermissionReport{
				Login:               "analytics",
				ProductMajorVersion: 16,
				MissingPermissions:  []string{"VIEW SERVER PERFORMANCE STATE"},
				GrantStatements:     []string{"GRANT VIEW SERVER PERFORMANCE STATE TO [analytics];"},
			}}
		}
		// The registry surfaces the permission failure as it came, so whatever
		// transport serves it can render the report rather than a flat string.
		_, err := r.Start(context.Background(), StartOptions{})
		Expect(err).To(HaveOccurred())
		var denied *xetrace.PermissionError
		Expect(errors.As(err, &denied)).To(BeTrue(), "a permission failure must stay recognisable: %v", err)
		Expect(denied.Report.Login).To(Equal("analytics"))
		Expect(denied.Report.MissingPermissions).To(ContainElement("VIEW SERVER PERFORMANCE STATE"))
	})

	// "ring buffer denied" is not in xetrace's transient allowlist, so it is
	// terminal on the first tick — which is what keeps this spec fast. If it
	// were ever classified transient, the retry budget (2 attempts x a 2s pause)
	// would blow Eventually's 1s default and this would flake rather than fail.
	ginkgo.It("records a poll failure and marks the trace stopped", func() {
		r := registryWithXE(failingWithPollErr(errors.New("ring buffer denied")))
		trace, err := r.Start(context.Background(), StartOptions{Poll: time.Millisecond})
		Expect(err).NotTo(HaveOccurred())

		Eventually(trace.Running).Should(BeFalse())
		Expect(trace.Error).To(ContainSubstring("ring buffer denied"))
		Expect(trace.StoppedAt).NotTo(BeZero())
		_, err = r.Stop(trace.ID)
		Expect(err).To(MatchError(ContainSubstring("ring buffer denied")))
	})

	ginkgo.It("aborts immediately when the event session has been dropped externally", func() {
		r := registryWithXE(failingWithPollErr(fmt.Errorf("%w: %q", xetrace.ErrSessionGone, "oipa_cli_trace_1_2")))
		trace, err := r.Start(context.Background(), StartOptions{Poll: time.Millisecond})
		Expect(err).NotTo(HaveOccurred())

		// Retrying a vanished session can never produce data, so it must fail
		// fast with a message that names the cause rather than a retry count.
		Eventually(trace.Running).Should(BeFalse())
		Expect(trace.Error).To(ContainSubstring("no longer present in sys.dm_xe_sessions"))
	})

	ginkgo.It("keeps capturing when a poll failure is transient", func() {
		properties.Set("sqltrace.poll.retryDelay", "1ms")
		ginkgo.DeferCleanup(func() { properties.Set("sqltrace.poll.retryDelay", "") })

		transient := mssql.StreamError{InnerError: fmt.Errorf(
			"did not get cancellation confirmation from the server (current response: %w)", context.DeadlineExceeded)}
		xe := &failingXE{pollErrs: []error{
			fmt.Errorf("read ring_buffer target: %w", transient),
			nil, // recovered
		}}
		r := registryWithXE(xe)

		trace, err := r.Start(context.Background(), StartOptions{Poll: time.Millisecond})
		Expect(err).NotTo(HaveOccurred())

		// A recovered blip must leave no terminal error behind: the trace is
		// still live, and Stop reports success.
		Consistently(func() string { return trace.Error }, 100*time.Millisecond).Should(BeEmpty())
		Expect(trace.Running()).To(BeTrue())
		_, err = r.Stop(trace.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(trace.Error).To(BeEmpty())
	})

	ginkgo.It("propagates session cleanup failures", func() {
		r := registryWithXE(&failingXE{dropErr: errors.New("drop denied")})
		trace, err := r.Start(context.Background(), StartOptions{Poll: time.Hour})
		Expect(err).NotTo(HaveOccurred())

		_, err = r.Stop(trace.ID)
		Expect(err).To(MatchError(ContainSubstring("drop denied")))
		Expect(trace.Error).To(ContainSubstring("drop denied"))
	})

	ginkgo.It("refuses to start without a cache store", func() {
		r := registryWithXE(&failingXE{})
		r.events = nil

		_, err := r.Start(context.Background(), StartOptions{})

		// Events live in Redis, so a registry without one can only pretend to
		// capture — it must say so rather than record nothing.
		Expect(err).To(MatchError(ContainSubstring("requires a cache store")))
	})
})
