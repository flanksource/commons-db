// Package pgproxy is the pg_proxy trace kind: a postgres wire proxy that
// processes under capture are pointed at through the environment the session
// exports. It records the SQL those processes run, which commons-db's own SQL
// tap never sees, as sql_statements rows.
package pgproxy

import (
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flanksource/commons-db/connection"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing"
	"github.com/flanksource/commons-db/tracing/sqlstatements"
)

// Kind is the kind a host registers NewKind as.
const Kind = "pg_proxy"

// maxValueBytes caps any one value a statement stores, such as its SQL.
const maxValueBytes = 64 << 10

// reportEvery is how often a running capture reports its summary.
const reportEvery = time.Second

// ProxyReport is what a running capture reports: the sql_statements summary of the
// statements it stored, and whether it stopped storing them.
type ProxyReport struct {
	sqlstatements.Summary
	Upstream  string `json:"upstream"`
	Truncated bool   `json:"truncated,omitempty"`
}

// running is what Prepare set up for Handle: the proxy, and the summary of the
// statements the capture stored, which the connection goroutines fold in.
type running struct {
	proxy     *proxy
	upstream  string
	truncated atomic.Bool

	mu      sync.Mutex
	summary *sqlstatements.Accumulator
}

func (r *running) add(row sqlstatements.StatementRow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.summary.Add(row)
}

func (r *running) report() ProxyReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return ProxyReport{Summary: r.summary.Result(), Upstream: r.upstream, Truncated: r.truncated.Load()}
}

type runningKey struct{}

type pgProxy struct{}

func (pgProxy) Params() Params {
	return Params{Connection: defaultConnection, MaxStatements: defaultMaxStatements}
}

func (pgProxy) Schema() recordresults.ResultType[sqlstatements.StatementRow] {
	return sqlstatements.ResultType()
}

// Prepare resolves the upstream, starts the proxy and exports the environment
// that points a process at it. The release drops every connection, waits for
// their pending statements to be stored and reports the final summary.
func (pgProxy) Prepare(ctx dbcontext.Context, params Params, records tracing.Emitter[sqlstatements.StatementRow]) (dbcontext.Context, func() error, error) {
	dsn, err := resolveDSN(params)
	if err != nil {
		return ctx, nil, err
	}
	capture := &running{upstream: upstreamAddr(dsn), summary: sqlstatements.NewAccumulator()}
	proxy, err := startProxy(params, dsn, capture.deliver(params, records))
	if err != nil {
		return ctx, nil, err
	}
	capture.proxy = proxy
	if err := capture.publish(records); err != nil {
		return ctx, nil, errors.Join(err, proxy.Close())
	}
	return ctx.WithValue(runningKey{}, capture), func() error {
		err := proxy.Close()
		records.Report(capture.report())
		return err
	}, nil
}

// deliver stores each statement the proxy decodes, without waiting: it arrives
// on a connection's goroutine. Statements beyond MaxStatements are not stored.
func (r *running) deliver(params Params, records tracing.Emitter[sqlstatements.StatementRow]) func(connection.Statement) {
	var seen atomic.Int64
	return func(statement connection.Statement) {
		if seen.Add(1) > int64(params.MaxStatements) {
			if r.truncated.CompareAndSwap(false, true) {
				records.Warn(fmt.Sprintf("pg_proxy stopped storing statements after maxStatements=%d", params.MaxStatements))
			}
			return
		}
		row := sqlstatements.FromStatement(statement)
		if records.TryEmit(row) {
			r.add(row)
		}
	}
}

// publish exports the child environment and names the session by the upstream
// it proxies, never carrying the DSN's password.
func (r *running) publish(records tracing.Emitter[sqlstatements.StatementRow]) error {
	env, err := r.proxy.childEnv()
	if err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(env)) {
		records.Export(name, env[name])
	}
	metadata := []query.SessionMetadata{{Name: "upstream", Label: "Upstream", Value: r.upstream}}
	if database := strings.TrimPrefix(r.proxy.dsn.Path, "/"); database != "" {
		metadata = append(metadata, query.SessionMetadata{Name: "database", Label: "Database", Value: database})
	}
	records.Describe(r.proxy.Addr(), metadata...)
	return nil
}

// Handle reports the summary every reportEvery until the capture stops: the
// proxy's connections emit every statement.
func (pgProxy) Handle(ctx dbcontext.Context, _ Params, records tracing.Emitter[sqlstatements.StatementRow], _ tracing.Records[sqlstatements.StatementRow]) error {
	capture, ok := ctx.Value(runningKey{}).(*running)
	if !ok {
		return errors.New("pg_proxy: the capture was not prepared")
	}
	ticker := time.NewTicker(reportEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			records.Report(capture.report())
		}
	}
}

// Summarize is a window's statements as sql_statements summarizes them.
func (pgProxy) Summarize(rows iter.Seq[sqlstatements.StatementRow]) (map[string]any, error) {
	return sqlstatements.WindowVars(rows), nil
}

// NewKind is the pg_proxy kind: statements masked and capped at 64KiB a
// value, as sql_statements stores them.
func NewKind() *tracing.Handler[Params, sqlstatements.StatementRow] {
	return tracing.NewHandler[Params, sqlstatements.StatementRow](pgProxy{}, tracing.Capabilities{Live: true}).
		WithSecretMasking().
		WithTruncation(maxValueBytes)
}
