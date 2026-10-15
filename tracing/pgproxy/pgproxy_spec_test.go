// Specs for the pg_proxy trace kind end to end: a client speaks the postgres
// protocol to the exported proxy address, a fake server answers, and the
// statements land in a Mark window as sql_statements rows.

package pgproxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/sqlstatements"
	"github.com/flanksource/commons-db/tracing/tracingtest"
)

// fakePostgres speaks just enough of the wire protocol to answer the messages
// a client sends: the recorder never interprets results, only the shape of the
// conversation. "fail" is answered with an ErrorResponse, INSERTs with a tag
// of one row, anything else with SELECT 3.
type fakePostgres struct{ listener net.Listener }

func startFakePostgres() *fakePostgres {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	Expect(err).ToNot(HaveOccurred())
	server := &fakePostgres{listener: listener}
	go server.serve()
	DeferCleanup(listener.Close)
	return server
}

func (f *fakePostgres) dsn() string {
	return "postgres://tester:s3cret@" + f.listener.Addr().String() + "/fixtures?sslmode=disable"
}

func (f *fakePostgres) serve() {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *fakePostgres) handle(conn net.Conn) {
	defer conn.Close()
	backend := pgproto3.NewBackend(conn, conn)
	if _, err := backend.ReceiveStartupMessage(); err != nil {
		return
	}
	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if backend.Flush() != nil {
		return
	}
	for {
		msg, err := backend.Receive()
		if err != nil {
			return
		}
		switch msg := msg.(type) {
		case *pgproto3.Query:
			answer(backend, msg.String)
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		case *pgproto3.Parse:
			backend.Send(&pgproto3.ParseComplete{})
		case *pgproto3.Bind:
			backend.Send(&pgproto3.BindComplete{})
		case *pgproto3.Execute:
			backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
		case *pgproto3.Sync:
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		case *pgproto3.Terminate:
			return
		}
		if backend.Flush() != nil {
			return
		}
	}
}

func answer(backend *pgproto3.Backend, sql string) {
	switch {
	case sql == "fail":
		backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Message: `relation "missing" does not exist`})
	case strings.HasPrefix(sql, "INSERT"):
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("INSERT 0 1")})
	default:
		backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 3")})
	}
}

// pgClient is the captured process, reduced to the protocol it speaks.
type pgClient struct {
	conn     net.Conn
	frontend *pgproto3.Frontend
}

func dialProxy(addr string) *pgClient {
	conn, err := net.Dial("tcp", addr)
	Expect(err).ToNot(HaveOccurred())
	DeferCleanup(conn.Close)
	client := &pgClient{conn: conn, frontend: pgproto3.NewFrontend(conn, conn)}
	client.frontend.Send(&pgproto3.StartupMessage{
		ProtocolVersion: pgproto3.ProtocolVersionNumber,
		Parameters:      map[string]string{"user": "tester", "database": "fixtures"},
	})
	client.flushUntilReady()
	return client
}

func (c *pgClient) flushUntilReady() {
	Expect(c.frontend.Flush()).To(Succeed())
	Expect(c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))).To(Succeed())
	for {
		msg, err := c.frontend.Receive()
		Expect(err).ToNot(HaveOccurred())
		if _, ready := msg.(*pgproto3.ReadyForQuery); ready {
			return
		}
	}
}

func (c *pgClient) query(sql string) {
	c.frontend.Send(&pgproto3.Query{String: sql})
	c.flushUntilReady()
}

func (c *pgClient) extended(sql string, params ...string) {
	c.frontend.Send(&pgproto3.Parse{Name: "s1", Query: sql})
	var values [][]byte
	for _, param := range params {
		values = append(values, []byte(param))
	}
	c.frontend.Send(&pgproto3.Bind{PreparedStatement: "s1", Parameters: values})
	c.frontend.Send(&pgproto3.Execute{})
	c.frontend.Send(&pgproto3.Sync{})
	c.flushUntilReady()
}

var _ = Describe("pg_proxy trace kind", func() {
	var env tracingtest.Env
	var server *fakePostgres

	BeforeEach(func() {
		env = tracingtest.NewEnv(map[string]tracing.TracePlugin{Kind: NewKind()})
		server = startFakePostgres()
	})

	start := func(params string) (*query.ManagedSession, map[string]string) {
		managed, err := env.Runtime.Start(dbcontext.New(), tracing.StartRequest{Kind: Kind, Params: []byte(params)})
		Expect(err).ToNot(HaveOccurred())
		tracingtest.Running(managed.Session())
		return managed, managed.Status().Exports
	}

	paramsFor := func(extra string) string {
		return fmt.Sprintf(`{"dsn": %q%s}`, server.dsn(), extra)
	}

	It("exports the environment that points a process at the proxy, without the password", func() {
		managed, exports := start(paramsFor(""))
		host, port, err := net.SplitHostPort(exports["PGHOST"] + ":" + exports["PGPORT"])
		Expect(err).ToNot(HaveOccurred())
		Expect(exports).To(And(
			HaveKeyWithValue("PGSSLMODE", "disable"), HaveKeyWithValue("PGDATABASE", "fixtures"),
			HaveLen(6),
		))
		for _, key := range []string{"GAVEL_DB_DSN", "DATABASE_URL"} {
			parsed, err := url.Parse(exports[key])
			Expect(err).ToNot(HaveOccurred(), key)
			Expect(parsed.Host).To(Equal(net.JoinHostPort(host, port)), key)
			Expect(parsed.Query().Get("sslmode")).To(Equal("disable"), key)
		}
		snapshot := managed.Session().Snapshot()
		Expect(snapshot.Handle).To(Equal(net.JoinHostPort(host, port)))
		Expect(fmt.Sprint(snapshot.Metadata)).ToNot(ContainSubstring("s3cret"))
		Expect(fmt.Sprint(snapshot.Metadata)).To(ContainSubstring(server.listener.Addr().String()))
	})

	It("stores simple, extended and failing statements of a Mark window with their row counts", func() {
		managed, exports := start(paramsFor(`, "params": true, "connection": "orders"`))
		client := dialProxy(net.JoinHostPort(exports["PGHOST"], exports["PGPORT"]))
		client.query("SELECT id FROM before_window")
		before, err := env.Runtime.Mark(context.Background(), managed)
		Expect(err).ToNot(HaveOccurred())

		client.query("SELECT id FROM users")
		client.query("INSERT INTO users (id) VALUES (1)")
		client.extended("SELECT * FROM users WHERE id = $1", "42")
		client.query("fail")
		Eventually(func() int64 {
			through, err := env.Runtime.Mark(context.Background(), managed)
			Expect(err).ToNot(HaveOccurred())
			return through - before
		}).WithTimeout(10 * time.Second).Should(BeEquivalentTo(4))
		through, err := env.Runtime.Mark(context.Background(), managed)
		Expect(err).ToNot(HaveOccurred())

		ref := tracing.TraceRef{Kind: Kind, Stream: managed.Session().Snapshot().Events.Stream, AfterSeq: before, ToSeq: through}
		var rows []sqlstatements.StatementRow
		Expect(tracing.ReadRecords(context.Background(), env.Runtime, ref, func(row sqlstatements.StatementRow) error {
			rows = append(rows, row)
			return nil
		})).To(Succeed())

		Expect(rows).To(HaveLen(4))
		Expect(rows[0]).To(And(
			HaveField("SQL", "SELECT id FROM users"), HaveField("Op", "SELECT"), HaveField("Tables", []string{"users"}),
			HaveField("Rows", BeEquivalentTo(3)), HaveField("Connection", "orders"), HaveField("Driver", "postgres"),
			HaveField("Error", BeEmpty()),
		))
		Expect(rows[1]).To(And(HaveField("Op", "INSERT"), HaveField("Rows", BeEquivalentTo(1))))
		Expect(rows[2]).To(And(HaveField("SQL", "SELECT * FROM users WHERE id = $1"), HaveField("Args", []any{"42"})))
		Expect(rows[3]).To(And(
			HaveField("Error", ContainSubstring(`relation "missing" does not exist`)), HaveField("Rows", BeEquivalentTo(-1)),
		))

		summary, err := env.Runtime.Summarize(context.Background(), ref)
		Expect(err).ToNot(HaveOccurred())
		Expect(summary).To(And(HaveKeyWithValue("statements", 4), HaveKeyWithValue("errors", 1), HaveKeyWithValue("by_op", HaveKeyWithValue("INSERT", 1))))
	})

	It("leaves out bind values unless params is set", func() {
		managed, exports := start(paramsFor(""))
		dialProxy(net.JoinHostPort(exports["PGHOST"], exports["PGPORT"])).extended("SELECT * FROM secrets WHERE token = $1", "hunter2")
		managed.Session().Stop("spec")
		rows := env.Rows(tracingtest.Ended(managed.Session()))
		Expect(rows).To(HaveLen(1))
		Expect(rows[0]["args"]).To(BeNil())
	})

	It("stops storing at maxStatements, warns and flags the report truncated", func() {
		managed, exports := start(paramsFor(`, "maxStatements": 2`))
		client := dialProxy(net.JoinHostPort(exports["PGHOST"], exports["PGPORT"]))
		for range 4 {
			client.query("SELECT 1")
		}
		managed.Session().Stop("spec")
		info := tracingtest.Ended(managed.Session())
		Expect(env.Rows(info)).To(HaveLen(2))
		Expect(info.Warning).To(ContainSubstring("maxStatements=2"))
		Expect(string(info.Summary)).To(ContainSubstring(`"truncated":true`))
	})

	It("refuses TLS negotiation as the protocol expects and still connects", func() {
		_, exports := start(paramsFor(""))
		conn, err := net.Dial("tcp", net.JoinHostPort(exports["PGHOST"], exports["PGPORT"]))
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(conn.Close)

		request := make([]byte, 8)
		binary.BigEndian.PutUint32(request[:4], 8)
		binary.BigEndian.PutUint32(request[4:], sslRequestCode)
		_, err = conn.Write(request)
		Expect(err).ToNot(HaveOccurred())
		Expect(conn.SetReadDeadline(time.Now().Add(10 * time.Second))).To(Succeed())
		answer := make([]byte, 1)
		_, err = io.ReadFull(conn, answer)
		Expect(err).ToNot(HaveOccurred())
		Expect(answer[0]).To(Equal(byte('N')))

		client := &pgClient{conn: conn, frontend: pgproto3.NewFrontend(conn, conn)}
		client.frontend.Send(&pgproto3.StartupMessage{
			ProtocolVersion: pgproto3.ProtocolVersionNumber, Parameters: map[string]string{"user": "tester", "database": "fixtures"},
		})
		client.flushUntilReady()
	})

	It("closes its listener and connections when the session ends", func() {
		managed, exports := start(paramsFor(""))
		addr := net.JoinHostPort(exports["PGHOST"], exports["PGPORT"])
		client := dialProxy(addr)
		managed.Session().Stop("spec")
		Expect(tracingtest.Ended(managed.Session()).Error).To(BeEmpty())

		Expect(client.conn.SetReadDeadline(time.Now().Add(5 * time.Second))).To(Succeed())
		_, err := client.frontend.Receive()
		Expect(err).To(HaveOccurred())
		_, err = net.DialTimeout("tcp", addr, time.Second)
		Expect(err).To(HaveOccurred())
	})

	DescribeTable("refuses params the capture cannot start with",
		func(params, wantErr string) {
			GinkgoT().Setenv("GAVEL_DB_DSN", "")
			GinkgoT().Setenv("DATABASE_URL", "")
			plugin, ok := env.Runtime.Kinds.Get(Kind)
			Expect(ok).To(BeTrue())
			Expect(plugin.ValidateParams([]byte(params))).To(MatchError(ContainSubstring(wantErr)))
		},
		Entry("no upstream anywhere", `{}`, "GAVEL_DB_DSN"),
		Entry("a non-postgres scheme", `{"dsn": "mysql://localhost/db"}`, "postgres://"),
		Entry("an unset $VAR", `{"dsn": "$PG_PROXY_UNSET_VAR"}`, "GAVEL_DB_DSN"),
		Entry("a blank connection label", `{"dsn": "postgres://h/db", "connection": " "}`, "connection"),
		Entry("a non-positive maxStatements", `{"dsn": "postgres://h/db", "maxStatements": -1}`, "maxStatements"),
	)

	It("resolves a $VAR reference, then GAVEL_DB_DSN, then DATABASE_URL", func() {
		GinkgoT().Setenv("PG_PROXY_SPEC_DSN", "postgres://expanded/db")
		GinkgoT().Setenv("GAVEL_DB_DSN", "postgresql://primary/db")
		GinkgoT().Setenv("DATABASE_URL", "postgres://fallback/db")
		for declared, host := range map[string]string{"$PG_PROXY_SPEC_DSN": "expanded", "": "primary"} {
			dsn, err := resolveDSN(declared)
			Expect(err).ToNot(HaveOccurred())
			Expect(dsn.Host).To(Equal(host))
		}
		GinkgoT().Setenv("GAVEL_DB_DSN", "")
		dsn, err := resolveDSN("")
		Expect(err).ToNot(HaveOccurred())
		Expect(dsn.Host).To(Equal("fallback"))
		Expect(upstreamAddr(dsn)).To(Equal("fallback:5432"))
	})
})
