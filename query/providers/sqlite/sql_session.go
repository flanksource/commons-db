package sqlite

import (
	stdcontext "context"
	"database/sql"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/query/sqlactivity"
)

// autoDiagnoseInterval is how often a statement that outran AutoDiagnose is
// sampled until it ends.
const autoDiagnoseInterval = 2 * time.Second

// sessionIdentity is the round trip that names the server session a pinned
// connection is on. numbered says the first column is the session id;
// ClickHouse has none, its query id names the statement instead.
type sessionIdentity struct {
	engine   string
	query    string
	numbered bool
}

// sessionIdentities holds every dialect with a server session; SQLite runs
// in process and has none.
var sessionIdentities = map[sqlDialect]sessionIdentity{
	dialectSQLServer: {engine: sqlactivity.EngineSQLServer, numbered: true,
		query: "SELECT CAST(@@SPID AS bigint), COALESCE(@@SERVERNAME, ''), COALESCE(DB_NAME(), ''), COALESCE(SUSER_SNAME(), '')"},
	dialectPostgres: {engine: sqlactivity.EnginePostgres, numbered: true,
		query: "SELECT pg_backend_pid()::bigint, COALESCE(host(inet_server_addr()), ''), current_database(), current_user"},
	dialectMySQL: {engine: "mysql", numbered: true,
		query: "SELECT CONNECTION_ID(), @@hostname, COALESCE(DATABASE(), ''), CURRENT_USER()"},
	dialectClickHouse: {engine: "clickhouse",
		query: "SELECT hostName(), currentDatabase(), currentUser()"},
}

// sampledEngines are the engines sqlactivity reads.
var sampledEngines = map[string]bool{sqlactivity.EnginePostgres: true, sqlactivity.EngineSQLServer: true}

// sessionGrants names what a login needs to see sessions other than its own,
// for the error that says why one was not visible.
var sessionGrants = map[string]string{
	sqlactivity.EngineSQLServer: "; the login needs VIEW SERVER STATE (GRANT VIEW SERVER STATE TO <login>) to read other sessions",
}

// queryer is what a statement runs on: the pool, or a session pinned from it.
type queryer interface {
	QueryContext(ctx stdcontext.Context, query string, args ...any) (*sql.Rows, error)
}

// sessionRequest is what identifying a statement's session needs. Page is the
// 1-based page of a paged read, zero for a program; QueryID is ClickHouse's
// id for the statement.
type sessionRequest struct {
	Dialect     sqlDialect
	Diagnostics *query.ProviderDiagnostics
	Page        int
	QueryID     string
}

// pinSession opens a session from the pool and records which server session
// it is. A session that cannot be identified runs nothing.
func pinSession(ctx stdcontext.Context, client *sql.DB, request sessionRequest) (*sql.Conn, query.ServerSession, error) {
	conn, err := client.Conn(ctx)
	if err != nil {
		return nil, query.ServerSession{}, fmt.Errorf("open a session for the statement: %w", err)
	}
	session, err := recordSession(ctx, conn, request)
	if err != nil {
		_ = conn.Close()
		return nil, query.ServerSession{}, err
	}
	return conn, session, nil
}

// recordSession reads and records the server session conn is on. SQLite has
// none, so nothing is recorded for it.
func recordSession(ctx stdcontext.Context, conn *sql.Conn, request sessionRequest) (query.ServerSession, error) {
	if request.Dialect == dialectSQLite {
		return query.ServerSession{}, nil
	}
	identity, ok := sessionIdentities[request.Dialect]
	if !ok {
		return query.ServerSession{}, fmt.Errorf("no server session identity for sql dialect %q", request.Dialect)
	}
	session := query.ServerSession{Engine: identity.engine, QueryID: request.QueryID, Page: request.Page}
	targets := []any{&session.Server, &session.Database, &session.User}
	if identity.numbered {
		targets = append([]any{&session.SessionID}, targets...)
	}
	if err := conn.QueryRowContext(ctx, identity.query).Scan(targets...); err != nil {
		return query.ServerSession{}, fmt.Errorf("identify the %s session: %w", identity.engine, err)
	}
	request.Diagnostics.RecordSession(session)
	return session, nil
}

// startAutoDiagnose samples session through the pool, a second connection,
// once the statement has run AutoDiagnoseAfter (at once when it is negative)
// and every autoDiagnoseInterval after, until stop. stop waits for the
// sampler, so no sample arrives after it returns.
func startAutoDiagnose(ctx stdcontext.Context, client *sql.DB, session query.ServerSession, diagnostics *query.ProviderDiagnostics) (stop func()) {
	after := diagnostics.AutoDiagnoseAfter()
	if after == 0 || session.SessionID == 0 || !sampledEngines[session.Engine] {
		return func() {}
	}
	after = max(after, 0)
	sampling, cancel := stdcontext.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		sampleSession(sampling, client, session, diagnostics, after)
	}()
	return func() {
		cancel()
		<-done
	}
}

// sampleSession samples until ctx ends or a sample fails. A failure stops the
// sampling and fails the diagnostics, never the statement.
func sampleSession(ctx stdcontext.Context, client *sql.DB, session query.ServerSession, diagnostics *query.ProviderDiagnostics, after time.Duration) {
	wait := time.NewTimer(after)
	defer wait.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wait.C:
		}
		if err := sampleOnce(ctx, client, session, diagnostics); err != nil {
			if ctx.Err() == nil {
				diagnostics.RecordSampleError(err)
			}
			return
		}
		wait.Reset(autoDiagnoseInterval)
	}
}

func sampleOnce(ctx stdcontext.Context, client *sql.DB, session query.ServerSession, diagnostics *query.ProviderDiagnostics) error {
	filter := sqlactivity.Filter{Idle: true, SessionID: session.SessionID}
	snapshot, err := sqlactivity.Read(ctx, client, session.Engine, filter)
	if err != nil {
		return fmt.Errorf("sample %s session %d%s: %w", session.Engine, session.SessionID, sessionGrants[session.Engine], err)
	}
	if len(snapshot.Rows) == 0 {
		return fmt.Errorf("%s session %d is not in the server's activity view%s", session.Engine, session.SessionID, sessionGrants[session.Engine])
	}
	for _, row := range snapshot.Rows {
		diagnostics.RecordSample(row)
	}
	return nil
}
