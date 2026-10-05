// The trace plugins a serving process runs: serve opens their results store,
// and from then on their result profiles and index connection join the catalog.

package app

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/models"
	"github.com/flanksource/commons-db/query/profilestore"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/probe"
	"github.com/flanksource/commons-db/recordstore/recordresults"
	"github.com/flanksource/commons-db/tracing/traces"
	"github.com/flanksource/commons-db/tracing/traces/httptraffic"
	"github.com/flanksource/commons-db/tracing/traces/opensearchtraces"
	"github.com/flanksource/commons-db/tracing/traces/sqlstatements"
	"github.com/flanksource/commons-db/tracing/traces/xevent"
)

// tracesPrefix names the trace results store: its <prefix>/<kind> profiles and
// its index connection, connection://traces/traces.
const tracesPrefix = "traces"

// traceKinds is the trace plugin kinds a server serves.
func traceKinds() (*traces.Kinds, error) {
	kinds := traces.NewKinds()
	for name, plugin := range map[string]traces.TracePlugin{
		"http":       httptraffic.Kind(),
		"opensearch": opensearchtraces.Kind(),
		"sql":        sqlstatements.Kind(),
		"sql_xevent": xevent.Kind(),
	} {
		if err := kinds.RegisterKind(name, plugin); err != nil {
			return nil, err
		}
	}
	return kinds, nil
}

// tracePlugins holds the trace runtime serve opens. Until it is open the
// profile catalog, profile reads and connection resolution pass it by, so a
// command that never serves never opens a trace store.
type tracePlugins struct {
	mu      sync.RWMutex
	runtime *traces.Runtime
}

// open opens the trace results store under configDir, declaring kinds' result
// types, and returns the runtime that starts their captures with the close
// that stops using the store.
func (t *tracePlugins) open(configDir string, retention time.Duration, kinds *traces.Kinds) (*traces.Runtime, func() error, error) {
	dir := filepath.Join(configDir, "traces")
	results, err := recordresults.Open(recordresults.OpenOptions{
		Prefix: tracesPrefix, ConnectionName: tracesPrefix,
		Settings: recordstore.Settings{Prefix: tracesPrefix, Backend: recordstore.BackendSQLite, Dir: dir, TTL: retention},
		Register: kinds.RegisterResultTypes,
	})
	if err != nil {
		return nil, nil, err
	}
	runtime := &traces.Runtime{
		Kinds: kinds, Results: results, Probes: probe.NewManager(probe.ManagerOptions{LockDir: filepath.Join(dir, "locks")}),
	}
	t.mu.Lock()
	t.runtime = runtime
	t.mu.Unlock()
	return runtime, results.Close, nil
}

// current is the open runtime, or nil before serve opens one.
func (t *tracePlugins) current() *traces.Runtime {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.runtime
}

// overlay adds the trace result profiles to store once the runtime is open.
func (t *tracePlugins) overlay(store profilestore.Store) (profilestore.Store, error) {
	runtime := t.current()
	if runtime == nil {
		return store, nil
	}
	return profiles.NewOverlayStore(store, runtime.Results.Registry)
}

// beforeExecute catches the trace streams a read names up into the index once
// the runtime is open, and passes every other read through.
func (t *tracePlugins) beforeExecute(ctx context.Context, reads []profilestore.ReadRequest) (func(), error) {
	runtime := t.current()
	if runtime == nil {
		return func() {}, nil
	}
	return runtime.Results.Registry.BeforeExecute(ctx, reads)
}

// resolveConnection resolves the trace index connection once the runtime is
// open, and every other reference through next.
func (t *tracePlugins) resolveConnection(next dbcontext.ConnectionResolver) dbcontext.ConnectionResolver {
	return func(ctx dbcontext.Context, reference string) (*models.Connection, error) {
		connection, err := next(ctx, reference)
		if connection != nil || err != nil {
			return connection, err
		}
		if runtime := t.current(); runtime != nil {
			return runtime.Results.Registry.ResolveConnection(ctx, reference)
		}
		return nil, nil
	}
}
