package sqltrace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	clickycache "github.com/flanksource/clicky/cache"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/kv"
	"github.com/flanksource/commons-db/recordstore/sqlite"
	"github.com/flanksource/commons-db/tracing/xetrace"
)

// fakeXE is an in-memory XESession that serves pre-loaded events and records
// drop calls. Batches are consumed in order; once exhausted, Poll returns nothing
// so the drain loop idles until its context is cancelled.
type fakeXE struct {
	mu      sync.Mutex
	batches [][]xetrace.Event
	// stats, when set, is the ring-buffer bookkeeping served with the batch of
	// the same index — and with every idle poll after the last one, as the
	// server's running totals would be.
	stats   []xetrace.TargetStats
	pollErr []error
	dropErr error
	calls   int
	dropped bool
}

func (f *fakeXE) Poll(context.Context) (xetrace.TargetSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := f.calls
	f.calls++
	if n := len(f.pollErr); n > 0 {
		if err := f.pollErr[min(call, n-1)]; err != nil {
			return xetrace.TargetSnapshot{}, err
		}
	}
	var stats xetrace.TargetStats
	if n := len(f.stats); n > 0 {
		stats = f.stats[min(call, n-1)]
	}
	if call >= len(f.batches) {
		return xetrace.TargetSnapshot{Stats: stats}, nil
	}
	return xetrace.TargetSnapshot{Events: f.batches[call], Stats: stats}, nil
}

func (f *fakeXE) Drop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropped = true
	return f.dropErr
}

func (f *fakeXE) wasDropped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dropped
}

var capturedAt = time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)

func statement(sid int, sql string, offset time.Duration) xetrace.Event {
	return xetrace.Event{
		Name: "sql_statement_completed", SessionID: sid, Statement: sql, SQL: sql,
		Timestamp: capturedAt.Add(offset), Duration: time.Millisecond, DatabaseName: "warehouse",
	}
}

// environmentKey carries the environment a context runs against, as a host
// serving several environments would.
type environmentKey struct{}

// environment is a record store for an environment named "lab": a Router opens
// one backend per environment named by the call's context, and refuses a
// context naming none.
type environment struct {
	backend recordstore.Backend
}

// newEnvironment opens the store over a real sqlite file, or, when l2 is set,
// over a kv backend in l2 keyed per environment.
func newEnvironment(l2 clickycache.Store) environment {
	dir := ginkgo.GinkgoT().TempDir()
	schemas := recordstore.NewSchemas()
	columns, err := query.ColumnsFor(reflect.TypeFor[EventRow]())
	Expect(err).ToNot(HaveOccurred())
	Expect(schemas.Register(RowKind, columns, recordstore.KindOptions{})).To(Succeed())
	router, err := recordstore.NewRouter(recordstore.RouterOptions{
		Route: func(ctx context.Context) (string, error) {
			name, ok := ctx.Value(environmentKey{}).(string)
			if !ok {
				return "", errors.New("the context names no environment")
			}
			return name, nil
		},
		Open: func(_ context.Context, route string) (recordstore.Backend, error) {
			if l2 != nil {
				return kv.New(kv.Options{Store: l2, Prefix: route + ":trace-results", Schema: schemas.Kind, TTL: time.Hour, MaxChunkBytes: 4 << 20})
			}
			return sqlite.Open(sqlite.Options{
				Path: filepath.Join(dir, route+".sqlite"), Schema: schemas.Kind, TTL: time.Hour, SweepInterval: time.Hour,
			})
		},
	})
	Expect(err).ToNot(HaveOccurred())
	ginkgo.DeferCleanup(router.Close)
	return environment{backend: router}
}

func (e environment) ctx() context.Context {
	return context.WithValue(context.Background(), environmentKey{}, "lab")
}

func (e environment) store() RecordStore { return backendStore(e) }

// statements reads back the SQL of every row stream holds, in seq order.
func (e environment) statements(stream string) []string {
	var out []string
	err := e.backend.Scan(e.ctx(), stream, 0, func(_ int64, row recordstore.Row) error {
		out = append(out, fmt.Sprint(row["sql"]))
		return nil
	})
	Expect(err).ToNot(HaveOccurred())
	return out
}

func (e environment) meta(stream string) recordstore.Meta {
	meta, err := e.backend.Meta(e.ctx(), stream)
	Expect(err).ToNot(HaveOccurred())
	return meta
}

// backendStore is a RecordStore straight over a backend, describing a stream
// from its own metadata the way the trace results store does.
type backendStore struct{ backend recordstore.Backend }

func (s backendStore) Append(ctx context.Context, stream, kind string, rows []recordstore.Row) (recordstore.AppendResult, error) {
	return s.backend.Append(ctx, stream, kind, rows)
}

func (s backendStore) Seal(ctx context.Context, stream string) error {
	return s.backend.Seal(ctx, stream)
}

func (s backendStore) EventsRef(ctx context.Context, stream string, from, to int64) (query.EventsRef, error) {
	meta, err := s.backend.Meta(ctx, stream)
	if err != nil {
		return query.EventsRef{}, err
	}
	if from == 0 {
		from = meta.LowSeq
	}
	if to == 0 {
		to = meta.HighSeq
	}
	return query.EventsRef{
		Stream: meta.Stream, Kind: meta.Kind, Generation: meta.Generation, Low: meta.LowSeq, High: meta.HighSeq,
		From: from, To: to, Total: meta.Total, ExpiresAt: meta.ExpiresAt,
	}, nil
}

// newTestRegistry builds a registry over env's real store and xe.
func newTestRegistry(store RecordStore, xe XESession) *Registry {
	registry, err := NewRegistry(RegistryOptions{
		DB:    func(context.Context) (*sql.DB, func(), error) { return nil, func() {}, nil },
		Store: store,
		NewSession: func(context.Context, *sql.DB, xetrace.CreateOptions) (XESession, Opened, error) {
			return xe, Opened{Name: "commons_db_trace_test"}, nil
		},
		CurrentDatabase: func(context.Context, *sql.DB) (string, error) { return "warehouse", nil },
	})
	Expect(err).ToNot(HaveOccurred())
	return registry
}

// gatedStore holds every non-empty append until release is closed, so a spec
// can observe what a reader sees while rows are queued but not committed.
type gatedStore struct {
	RecordStore
	release chan struct{}
	failed  error
}

func (g *gatedStore) Append(ctx context.Context, stream, kind string, rows []recordstore.Row) (recordstore.AppendResult, error) {
	if len(rows) > 0 {
		<-g.release
		if g.failed != nil {
			return recordstore.AppendResult{}, g.failed
		}
	}
	return g.RecordStore.Append(ctx, stream, kind, rows)
}

// spyStore records every key written to a kv store.
type spyStore struct {
	clickycache.Store
	mu   sync.Mutex
	keys []string
}

func (s *spyStore) record(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
}

func (s *spyStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	s.record(key)
	return s.Store.Set(ctx, key, value, ttl)
}

func (s *spyStore) ZAdd(ctx context.Context, key string, score float64, member string) error {
	s.record(key)
	return s.Store.ZAdd(ctx, key, score, member)
}

func (s *spyStore) written() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.keys...)
}

func containingAny(keys []string, fragment string) []string {
	var out []string
	for _, key := range keys {
		if strings.Contains(key, fragment) {
			out = append(out, key)
		}
	}
	return out
}
